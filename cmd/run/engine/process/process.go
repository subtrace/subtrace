// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package process

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/engine/seccomp"
	"subtrace.dev/cmd/run/fd"
	"subtrace.dev/cmd/run/socket"
	"subtrace.dev/event"
	"subtrace.dev/global"
)

type Process struct {
	global *global.Global

	PID    int
	Exited chan struct{}

	pidfd   *fd.FD
	mu      sync.RWMutex
	sockets map[int]*socket.Socket
	links   map[string]string

	tmpl atomic.Pointer[event.Event]
}

// New creates a new process with the given PID.
func New(global *global.Global, pid int) (*Process, error) {
	ret, _, errno := unix.Syscall(unix.SYS_PIDFD_OPEN, uintptr(pid), 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("pidfd_open %d: %w", pid, errno)
	}
	pidfd := fd.NewFD(int(ret))
	defer pidfd.DecRef()

	return &Process{
		global: global,

		PID:    pid,
		Exited: make(chan struct{}),

		pidfd:   pidfd,
		sockets: make(map[int]*socket.Socket),
		links:   make(map[string]string),
	}, nil
}

func (p *Process) getEventTemplate() *event.Event {
	if tmpl := p.tmpl.Load(); tmpl != nil {
		return tmpl
	}

	tmpl := p.global.EventTemplate.Copy()

	tmpl.Set("process_id", fmt.Sprintf("%d", p.PID))

	if path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", p.PID)); err == nil {
		tmpl.Set("process_executable_name", filepath.Base(path))
	}

	if info, err := os.Stat(fmt.Sprintf("/proc/%d/exe", p.PID)); err == nil {
		tmpl.Set("process_executable_size", fmt.Sprintf("%d", info.Size()))
	}

	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.PID)); err == nil {
		var parts []string
		args := bytes.Split(cmdline, []byte{0})
		for i := 0; i < len(args)-1; i++ {
			parts = append(parts, string(args[i]))
		}
		tmpl.Set("process_command_line", strings.Join(parts, " "))
	}

	if info, err := os.Stat(fmt.Sprintf("/proc/%d", p.PID)); err == nil {
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			if name, err := findUsername(sys.Uid); err == nil && name != "" {
				tmpl.Set("process_user", name)
			} else {
				tmpl.Set("process_user", fmt.Sprintf("%d", sys.Uid))
			}
		}
	}

	p.tmpl.Store(tmpl)
	return tmpl
}

func (p *Process) LogValue() slog.Value {
	select {
	case <-p.Exited:
		return slog.GroupValue(slog.Int("pid", p.PID), slog.Bool("exited", true))
	default:
		return slog.GroupValue(slog.Int("pid", p.PID), slog.Bool("exited", false))
	}
}

// installSocket installs a socket into the process's file descriptor table and
// completes the seccomp notification.
func (p *Process) installSocket(n *seccomp.Notif, sock *socket.Socket, flags int) error {
	if !sock.FD.IncRef() {
		return unix.EBADF
	}
	defer sock.FD.DecRef()

	// Acquire the mutex because we want all subsequent getSocket calls to see
	// that this is a socket we care about. Since the tracer engine may have
	// multiple concurrent workers, we need synchronization until the end of this
	// function.
	p.mu.Lock()
	defer p.mu.Unlock()

	fd, err := n.AddFD(sock.FD, flags)
	if err != nil {
		return fmt.Errorf("addfd: %w", err)
	}
	if p.sockets[fd] != nil {
		return fmt.Errorf("register: socket already exists")
	}
	p.sockets[fd] = sock

	slog.Debug("registered socket", "proc", p, "sock", sock, "fd", fmt.Sprintf("targfd_%d", fd))
	return nil
}

func (p *Process) getSocket(fd int) (*socket.Socket, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sockets[fd]
	if !ok {
		return nil, false
	}
	return s, ok
}

func (p *Process) getDeleteSocket(fd int) (*socket.Socket, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sockets[fd]
	if !ok {
		return nil, false
	}
	delete(p.sockets, fd)
	return s, ok
}

func (p *Process) getFD(targetFD int) (*fd.FD, syscall.Errno) {
	if !p.pidfd.IncRef() {
		return nil, unix.EBADF
	}
	defer p.pidfd.DecRef()

	ret, _, errno := unix.Syscall(unix.SYS_PIDFD_GETFD, uintptr(p.pidfd.FD()), uintptr(targetFD), 0)
	if errno != 0 {
		return nil, errno
	}
	return fd.NewFD(int(ret)), 0
}

func (p *Process) poll() (exited bool, _ error) {
	if !p.pidfd.IncRef() {
		return false, fmt.Errorf("pidfd: file closed")
	}
	defer p.pidfd.DecRef()

	fds := []unix.PollFd{{Fd: int32(p.pidfd.FD()), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, -1)
	if err == unix.EINTR {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return fds[0].Revents&unix.POLLIN != 0, nil
}

// Wait waits for the process to exit and cleans up resources in the end.
func (p *Process) Wait() error {
	for {
		// We use a poll on the pidfd because the usual wait4(2) way doesn't let us
		// wait on non-children processes (see https://stackoverflow.com/a/1157739).
		exited, err := p.poll()
		if err != nil {
			if errors.Is(err, unix.EBADF) {
				select {
				case <-p.Exited:
					return nil
				default:
				}
			}
			return fmt.Errorf("poll: %w", err)
		}
		if exited {
			break
		}
	}

	if p.markAsExited() {
		// If a process exits with exit(2) or exit_group(2), handleExit is
		// responsible for the cleanup. We call cleanup only for processes that
		// do not exit cleanly (ex: SIGTERM, SIGKILL).
		go p.cleanup()
	}
	return nil
}

// markAsExited marks the process as exited. If the process has already been
// marked as exited by someone else, it returns false, otherwise true.
func (p *Process) markAsExited() (marked bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	select {
	case <-p.Exited:
		// markAsExited may be called by the pidfd poll (see Wait above) and/or the
		// SYS_EXIT handler. If SYS_EXIT happens before Wait, the SYS_EXIT call
		// should get preference. This function must still be called Wait to free
		// allocated resources because a call from SYS_EXIT isn't guaranteed since
		// processes may not necessarily exit cleanly every time (ex: SIGKILL).
		return false
	default:
		close(p.Exited)
		return true
	}
}

func (p *Process) cleanup() {
	// TODO: is this right? what if a FD we hold is held by another process with
	// a dup+sendmsg or a pidfd_getfd?
	<-p.Exited

	p.mu.RLock()
	defer p.mu.RUnlock()
	var errs []error
	for fd, s := range p.sockets {
		if errno := s.Close(); errno != 0 {
			errs = append(errs, fmt.Errorf("close socket fd=targfd_%d: %w", fd, errno))
		}
	}

	if !p.pidfd.ClosingIncRef() {
		errs = append(errs, fmt.Errorf("pidfd: already closed"))
	} else {
		defer p.pidfd.DecRef()
		p.pidfd.Lock()
		if err := unix.Close(p.pidfd.FD()); err != nil {
			errs = append(errs, fmt.Errorf("pidfd: close: %w", err))
		}
	}

	for idx, err := range errs {
		slog.Error("failed to clean up process", "process", p, "idx", idx, "err", err)
	}
}

func findUsername(uid uint32) (string, error) {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 7 {
			continue
		}
		parsed, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		if parsed == int(uid) {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("not found")
}

func htons(x uint16) uint16 { return (x&0xff)<<8 | (x >> 8) }
