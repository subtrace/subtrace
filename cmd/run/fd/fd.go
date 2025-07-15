// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

// Package fd contains a reference counted container for file descriptor.
//
// To prevent misuse, never store raw operating system file descriptor numbers.
// It's too easy to close a file descriptor with lingering references still in
// memory. Doing so can have catastrophic consequences on unrelated files and
// sockets since OS file descriptor numbers are often recycled. Using this
// struct and calling (*FD).FD() within IncRef/DecRef guards to access the
// underlying FD value is safer. It's still totally possible to shoot yourself
// in the foot (for example, using (*FD).FD() as a map[int]any key), but it's a
// tiny bit harder now.
//
// This package is mostly inspired by the Go runtime's internal/poll.FD [1] and
// gvisor's pkg/refs [2], but it should be noted that although this package is
// simpler than both, it's not nearly as prod tested as either.
//
// [1] https://github.com/golang/go/blob/6db1102605f227093ea95538f0fe9e46022ad7ea/src/internal/poll/fd_unix.go
// [2] https://github.com/google/gvisor/blob/0bb4cfdc4bc41d8378df96c83504f3195bc29841/pkg/refs/refcounter.go
package fd

import (
	"fmt"
	"sync/atomic"
	_ "unsafe"
)

const DEBUG = false

const (
	flagClosed = 1 << 31
	maxRefs    = 1 << 24
)

// FD is a reference counted file descriptor.
//
// TODO: add verbose logging for better debuggability of ref leaks
type FD struct {
	fd   uint32
	refs uint32
	sema uint32

	origFD int // purely for logging purposes

	_ func() // no copy
}

// NewFD returns a FD with the reference counter initialized to 1.
func NewFD(fd int) *FD {
	return &FD{fd: uint32(fd), refs: 1, origFD: fd}
}

func (fd *FD) String() string {
	n := atomic.LoadUint32(&fd.fd)
	if n == ^uint32(0) {
		return fmt.Sprintf("subfd_%d[closed]", fd.origFD)
	}
	return fmt.Sprintf("subfd_%d", n)
}

// IncRef increments the ref counter. If fd was closed, it's a no-op and returns false.
func (fd *FD) IncRef() bool {
	refs := atomic.AddUint32(&fd.refs, 1)
	left := refs &^ uint32(flagClosed)
	if DEBUG {
		fmt.Printf("%p: %s: IncRef: left=%d, closed=%v\n", fd, fd.String(), left, refs&flagClosed)
	}
	if refs&flagClosed != 0 {
		fd.decRef(true)
		return false
	}
	if refs >= maxRefs {
		panic(fmt.Sprintf("too many concurrent file descriptor references (max %d)", maxRefs))
	}
	return true
}

// MustIncRef adds a reference to fd. It panics when fd is closed.
func (fd *FD) MustIncRef() {
	if !fd.IncRef() {
		panic("file closed")
	}
}

// FD returns the underlying operating system file descriptor number.
func (fd *FD) FD() int {
	val := atomic.LoadUint32(&fd.fd)
	if val == ^uint32(0) {
		panic("file descriptor misuse outside IncRef/DecRef guards: file closed")
	}
	return int(val)
}

func (fd *FD) decRef(internal bool) {
	refs := atomic.AddUint32(&fd.refs, ^uint32(0))
	left := refs &^ uint32(flagClosed)
	if DEBUG {
		fmt.Printf("%p: %s: DecRef: left=%d, closed=%v\n", fd, fd.String(), left, refs&flagClosed)
	}
	if left >= maxRefs {
		panic(fmt.Sprintf("ref counter underflow: %08x", left))
	}
	if refs&flagClosed != 0 {
		switch left {
		case 1:
			// If this is the last non-closing DecRef, do a semrelease so that a
			// pending semacquire call (if any) can wake up.
			semrelease(&fd.sema, false, 0)
		case 0:
			// If we decremented the counter to zero, this is the closing DecRef.
			val := atomic.SwapUint32(&fd.fd, ^uint32(0))
			if val == ^uint32(0) {
				if !internal {
					panic("invalid closed file descriptor state: found fd=-1 in DecRef to zero")
				}
			}
		}
	}
}

// DecRef decrements the ref counter.
//
// If this call to DecRef corresponds to a ClosingIncRef, future fd.FD() calls
// will panic, so remember to call Lock between ClosingIncRef and DecRef.
func (fd *FD) DecRef() {
	fd.decRef(false)
}

// ClosingIncRef tries to increment the ref counter and mark fd as closed
// atomically. If fd was already closed or is being closed by a different
// goroutine, it returns false; otherwise, it returns true.
//
// Note that neither ClosingIncRef nor the corresponding DecRef will close the
// underlying OS file descriptor. It's the caller's responsibility to close(2)
// the file inbetween ClosingIncRef and the final DecRef call.
func (fd *FD) ClosingIncRef() bool {
	for {
		refs := atomic.LoadUint32(&fd.refs)
		if refs&flagClosed != 0 {
			return false
		}
		if atomic.CompareAndSwapUint32(&fd.refs, refs, flagClosed|(refs+1)) {
			return true
		}
	}
}

// Lock waits until there is exactly one pending ref (the caller's). It panics
// if the *fd.FD isn't already in the closed state. In most situations, Lock
// should be called after the ClosingIncRef and before the close(2) syscall.
func (fd *FD) Lock() {
	refs := atomic.LoadUint32(&fd.refs)
	left := refs &^ uint32(flagClosed)
	if refs&flagClosed == 0 {
		panic("Lock called without marking file as closed")
	}
	if DEBUG {
		fmt.Printf("%p: %s: Lock: left=%d, closed=%v\n", fd, fd.String(), left, refs&flagClosed)
	}
	if left > 1 {
		// The last non-closing DecRef will do a semrelease.
		semacquire(&fd.sema)
	}
}

//go:linkname semacquire sync.runtime_Semacquire
func semacquire(addr *uint32)

//go:linkname semrelease sync.runtime_Semrelease
func semrelease(addr *uint32, handoff bool, skipframes int)
