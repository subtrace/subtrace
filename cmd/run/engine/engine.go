// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/engine/process"
	"subtrace.dev/cmd/run/engine/seccomp"
	"subtrace.dev/cmd/run/socket"
	"subtrace.dev/cmd/run/syscalls"
	"subtrace.dev/cmd/version"
	"subtrace.dev/global"
)

type Engine struct {
	global  *global.Global
	seccomp *seccomp.Listener
	itab    *socket.InodeTable

	mu        sync.RWMutex
	processes map[int]*process.Process
	threads   map[int]*process.Process
	running   chan struct{}
	inPanic   atomic.Bool
}

func New(global *global.Global, seccomp *seccomp.Listener, itab *socket.InodeTable, root *process.Process) *Engine {
	e := &Engine{
		global:  global,
		seccomp: seccomp,
		itab:    itab,

		processes: map[int]*process.Process{root.PID: root},
		threads:   map[int]*process.Process{},
		running:   make(chan struct{}),
	}
	go e.waitProcess(root)
	return e
}

func (e *Engine) ensureProcessLocked(pid int) (*process.Process, error) {
	if _, ok := e.processes[pid]; !ok {
		tgid, err := getThreadGroupID(pid)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
				return nil, fmt.Errorf("get thread group: %w: %w", process.ErrProcessNotFound, err)
			}
			return nil, fmt.Errorf("get thread group: %w", err)
		}
		if tgid != pid {
			leader, err := e.ensureProcessLocked(tgid)
			if err != nil {
				return nil, fmt.Errorf("ensure process: %w", err)
			}

			e.threads[pid] = leader
			return leader, nil
		}

		p, err := process.New(e.global, e.itab, pid)
		if err != nil {
			return nil, fmt.Errorf("new process: %w", err)
		}

		slog.Debug("observed new process", "proc", p)

		// Import the new process' known inodes as sockets. We do this with the
		// engine locked because this needs to happen exactly once for each process
		// and must happen before handling the process' first syscall.
		if err := e.importInodes(p); err != nil {
			if !p.IsRunning() {
				return nil, fmt.Errorf("new process %d: import inodes: %w: %w", p.PID, process.ErrProcessNotFound, err)
			}
			return nil, fmt.Errorf("new process %d: import inodes: %w", p.PID, err)
		}

		e.processes[pid] = p
		go e.waitProcess(p)
	}

	return e.processes[pid], nil
}

func (e *Engine) importInodes(p *process.Process) error {
	dirfd, err := unix.Open(fmt.Sprintf("/proc/%d/fd/", p.PID), unix.O_RDONLY, 0o700)
	if err != nil {
		return fmt.Errorf("open dir: %w", err)
	}
	defer unix.Close(dirfd)

	count := 0
	for buf := make([]byte, 4096, 4096); ; {
		n, _, errno := unix.Syscall(unix.SYS_GETDENTS64, uintptr(dirfd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if errno != 0 {
			return fmt.Errorf("getdents64: %w", errno)
		}
		if n == 0 {
			break
		}

		for offset := 0; offset < int(n); {
			dirent := (*unix.Dirent)(unsafe.Pointer(&buf[offset]))
			offset += int(dirent.Reclen)

			if dirent.Type != unix.DT_LNK {
				continue
			}

			var stat unix.Stat_t
			if errno := fstatat(dirfd, dirent, &stat); errno != 0 {
				return fmt.Errorf("fstatat: %w", errno)
			}

			if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
				continue
			}

			inode, ok := e.itab.Get(stat.Ino)
			if !ok {
				continue
			}

			targetFD, valid := 0, true
			for i := 0; i < len(dirent.Name); i++ {
				if dirent.Name[i] == '\x00' {
					break
				}

				if !(dirent.Name[i] >= '0' && dirent.Name[i] <= '9') {
					valid = false
					break
				}

				targetFD *= 10
				targetFD += int(dirent.Name[i] - '0')
			}
			if !valid {
				continue
			}

			if err := p.ImportInode(targetFD, inode); err != nil {
				return fmt.Errorf("import %d: %w", inode.Number, err)
			}

			count += 1
		}
	}

	if count > 0 {
		slog.Debug("imported inodes at process init", "proc", p, "count", count)
	}
	return nil
}

func (e *Engine) waitProcess(p *process.Process) {
	if err := p.Wait(); err != nil {
		slog.Error("failed to wait for process", "proc", p, "err", err)
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.processes, p.PID)
	if len(e.processes) == 0 {
		if err := e.closeLocked(); err != nil {
			slog.Error("failed to close engine after all processes exited", "err", err)
		}
		slog.Debug("closed engine after all processes exited")
	}
}

func (e *Engine) getProcessFast(pid int) *process.Process {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if p, ok := e.processes[pid]; ok {
		return p
	}
	if p, ok := e.threads[pid]; ok {
		return p
	}
	return nil
}

func (e *Engine) getProcess(pid int) (*process.Process, error) {
	if p := e.getProcessFast(pid); p != nil {
		return p, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.ensureProcessLocked(pid)
	if err != nil {
		return nil, fmt.Errorf("ensure process: %w", err)
	}
	return p, nil
}

func (e *Engine) countRunning() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.processes)
}

func (e *Engine) closeLocked() error {
	select {
	case <-e.running:
		return nil
	default:
	}
	defer close(e.running)
	if err := e.seccomp.Close(); err != nil {
		return fmt.Errorf("close seccomp: %w", err)
	}
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeLocked()
}

func (e *Engine) Wait() {
	<-e.running
}

func (e *Engine) panicGuard(main, failed chan *seccomp.Notif) {
	err := recover()
	if err == nil {
		return
	}

	stack := debug.Stack()
	e.inPanic.Store(true)

	b := new(bytes.Buffer)
	fmt.Fprintf(b, "subtrace: engine panic: %v\n", err)
	fmt.Fprintf(b, "\n")
	fmt.Fprintf(b, "-----BEGIN SUBTRACE CRASH REPORT-----\n")
	fmt.Fprintf(b, "time: %s\n", time.Now().Format(time.RFC3339Nano))
	fmt.Fprintf(b, "version: %s\n", version.Full(false))
	fmt.Fprintf(b, "panic: %+v\n", err)
	fmt.Fprintf(b, "stack trace: %s\n", strings.TrimSpace(string(stack)))
	fmt.Fprintf(b, "-----END SUBTRACE CRASH REPORT-----\n")
	fmt.Fprintf(b, "\n")
	fmt.Fprintf(b, "[CRITICAL] !!! Subtrace encountered a critical error. The tracing engine will\n")
	fmt.Fprintf(b, "[CRITICAL] !!! now enter safe mode. New requests will no longer be traced.\n")
	fmt.Fprintf(b, "[CRITICAL] !!! We're sorry about this. Please consider filing a bug report at\n")
	fmt.Fprintf(b, "[CRITICAL] !!! https://github.com/subtrace/subtrace with the above crash report.\n")
	fmt.Fprintf(b, "\n")

	// Write all the bytes at once so that there's as little interference between
	// different processes writing to stderr at the same time. The tracer NEVER
	// writes to stdout or stderr for this exact reason (unless it is started
	// with -v or -log=true), but this is the exception to the rule.
	os.Stderr.Write(b.Bytes())

	go e.drainSafeMode(failed)
	e.drainSafeMode(main)
}

func (e *Engine) drainSafeMode(ch chan *seccomp.Notif) {
	for n := range ch {
		if n != nil {
			n.Skip()
		}
	}
}

func (e *Engine) handle(n *seccomp.Notif) {
	handler := process.Handlers[n.Syscall]
	if handler == nil {
		slog.Error(fmt.Sprintf("no handler found for %s", syscalls.GetName(n.Syscall)))
		return
	}

	p, err := e.getProcess(n.PID)
	var err2 error
	switch {
	case err == nil:
		err2 = handler(p, n)
	case errors.Is(err, process.ErrProcessNotFound):
		// At this point we can't call handler(p, n) since p is nil.
		// Instead, tell the kernel to handle the syscall the way it normally would.
		slog.Debug("handle: process not found", "notif", n, "error", err)
		err2 = n.Skip()
	default:
		panic(fmt.Errorf("get process: %w", err))
	}

	switch {
	case err2 == nil:
	case errors.Is(err2, seccomp.ErrCancelled):
		// The target's syscall was probably interrupted by a signal. We
		// don't need to do anything more here.
	default:
		slog.Error(fmt.Sprintf("critical error in handling %s", syscalls.GetName(n.Syscall)), "notif", n, "proc", p, "err", err2)
	}
}

// Start receives and handles intercepted syscalls until all processes exit.
func (e *Engine) Start() {
	N := runtime.NumCPU()

	var wg sync.WaitGroup
	slog.Debug("starting parallel receive-dispatch-handle loop", "workers", N)
	defer slog.Debug("finished parallel receive-dispatch-handle loop")

	failed := make(chan *seccomp.Notif, N)
	ch := make(chan *seccomp.Notif, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// TODO: sched_setaffinity to lock to CPU here? It'd be nice to have the
			// system call handler run on the same CPU as the tracee process that is
			// executing the system call.
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			defer e.panicGuard(ch, failed)

			var pending *seccomp.Notif
			defer func() {
				if pending != nil {
					failed <- pending
				}
			}()

			for n := range ch {
				if n == nil {
					break
				}

				pending = n
				if e.inPanic.Load() {
					return
				}
				e.handle(n)
				pending = nil
			}
		}()
	}

dispatch:
	for e.countRunning() > 0 {
		n, errno := e.seccomp.Receive()
		switch errno {
		case 0:
			ch <- n
		case unix.ENOENT:
			// The target was killed by a signal or its syscall was interrupted by a
			// signal handler.
			continue
		case unix.EBADF:
			// The seccomp listener file descriptor was closed.
			break dispatch
		default:
			if left := e.countRunning(); left > 0 {
				slog.Error("failed to receive seccomp notification", "processes", left, "err", errno)
			}
			break dispatch
		}
	}

	for i := 0; i < N; i++ {
		ch <- nil
	}
	wg.Wait()
}

func getThreadGroupID(pid int) (int, error) {
	path := fmt.Sprintf("/proc/%d/status", pid)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == "Tgid" {
			tgid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, fmt.Errorf("parse tgid: %w", err)
			}
			return tgid, nil
		}
	}
	return 0, fmt.Errorf("parse tgid: row not found")
}
