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

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/engine/process"
	"subtrace.dev/cmd/run/engine/seccomp"
	"subtrace.dev/cmd/run/syscalls"
	"subtrace.dev/cmd/version"
	"subtrace.dev/global"
)

type Engine struct {
	global  *global.Global
	seccomp *seccomp.Listener

	mu        sync.RWMutex
	processes map[int]*process.Process
	threads   map[int]*process.Process
	running   chan struct{}
	inPanic   atomic.Bool
}

func New(global *global.Global, seccomp *seccomp.Listener, root *process.Process) *Engine {
	e := &Engine{
		global:  global,
		seccomp: seccomp,

		processes: map[int]*process.Process{root.PID: root},
		threads:   map[int]*process.Process{},
		running:   make(chan struct{}),
	}
	go e.waitProcess(root)
	return e
}

func (e *Engine) ensureProcessLocked(pid int) *process.Process {
	if _, ok := e.processes[pid]; !ok {
		tgid, err := getThreadGroupID(pid)
		if err != nil {
			panic(fmt.Errorf("read process: %w", err))
		}
		if tgid != pid {
			leader := e.ensureProcessLocked(tgid)
			e.threads[pid] = leader
			return leader
		}

		e.processes[pid], err = process.New(e.global, pid)
		if err != nil {
			panic(fmt.Errorf("new process: %w", err))
		}
		go e.waitProcess(e.processes[pid])
	}

	return e.processes[pid]
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

func (e *Engine) getProcess(pid int) *process.Process {
	if p := e.getProcessFast(pid); p != nil {
		return p
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureProcessLocked(pid)
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

	p := e.getProcess(n.PID)
	switch err := handler(p, n); {
	case err == nil:
	case errors.Is(err, seccomp.ErrCancelled):
		// The target's syscall was probably interrupted by a signal. We
		// don't need to do anything more here.
	default:
		slog.Error(fmt.Sprintf("critical error in handling %s", syscalls.GetName(n.Syscall)), "notif", n, "proc", p, "err", err)
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
