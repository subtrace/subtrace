// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package process

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/engine/seccomp"
	"subtrace.dev/cmd/run/fd"
	"subtrace.dev/cmd/run/socket"
)

type Process struct {
	PID    int
	Exited chan struct{}

	pidfd   *fd.FD
	mu      sync.Mutex
	sockets map[int]*socket.Socket
	links   map[string]string
}

// New creates a new process with the given PID.
func New(pid int) (*Process, error) {
	ret, _, errno := unix.Syscall(unix.SYS_PIDFD_OPEN, uintptr(pid), 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("pidfd_open %d: %w", pid, errno)
	}
	pidfd := fd.NewFD(int(ret))
	defer pidfd.DecRef()

	return &Process{
		PID:    pid,
		Exited: make(chan struct{}),

		pidfd:   pidfd,
		sockets: make(map[int]*socket.Socket),
		links:   make(map[string]string),
	}, nil
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

	fd, err := n.AddFD(sock.FD, flags)
	if err != nil {
		return fmt.Errorf("addfd: %w", err)
	}

	// Syscall processing is synchronous, so it's safe to do this non-atomically
	// after AddFD. In the future, we may want to process syscalls async for
	// better performance; if so, we'd need a global lock to do this safely so
	// that syscalls after AddFD but before setSocket that refer to the installed
	// socket don't get ignored.
	if err := p.registerSocket(fd, sock); err != nil {
		return fmt.Errorf("register installed socket internally: %w", err)
	}
	slog.Debug("registered socket", "proc", p, "sock", sock, "fd", fmt.Sprintf("targfd_%d", fd))
	return nil
}

func (p *Process) getSocket(fd int, remove bool) (*socket.Socket, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sockets[fd]
	if !ok {
		return nil, false
	}
	if remove {
		delete(p.sockets, fd)
	}
	return s, ok
}

func (p *Process) registerSocket(fd int, sock *socket.Socket) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sockets[fd] != nil {
		return fmt.Errorf("already exists")
	}
	p.sockets[fd] = sock
	return nil
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
	p.mu.Lock()
	defer p.mu.Unlock()

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

	p.mu.Lock()
	defer p.mu.Unlock()
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

	for _, err := range errs {
		slog.Error("failed to clean up process", "err", err)
	}
}

func htons(x uint16) uint16 { return (x&0xff)<<8 | (x >> 8) }
