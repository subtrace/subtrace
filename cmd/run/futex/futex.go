// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package futex

import (
	"sync/atomic"
	"unsafe"

	"gvisor.dev/gvisor/pkg/abi/linux"
)

//go:linkname runtime_entersyscallblock runtime.entersyscallblock
func runtime_entersyscallblock()

//go:linkname runtime_exitsyscall runtime.exitsyscall
func runtime_exitsyscall()

//go:linkname runtime_futex runtime.futex
func runtime_futex(addr unsafe.Pointer, op int32, val uint32, ts, addr2 unsafe.Pointer, val3 uint32) int32

func futex(addr unsafe.Pointer, op int32, val uint32, ts, addr2 unsafe.Pointer, val3 uint32) int {
	runtime_entersyscallblock()
	ret := runtime_futex(addr, op, val, ts, addr2, val3)
	runtime_exitsyscall()
	return int(ret)
}

// Wait waits until the value at addr is not val.
func Wait(addr unsafe.Pointer, val uint32) {
	for { // the futex syscall may return spuriously, so check this in a loop
		futex(addr, linux.FUTEX_WAIT, val, nil, nil, 0)
		if atomic.LoadUint32((*uint32)(addr)) != val {
			return
		}
	}
}

// Wake wakes up at most count waiters waiting on addr. It returns the number
// of waiters woken up.
func Wake(addr unsafe.Pointer, count int) int {
	return futex(addr, linux.FUTEX_WAKE, uint32(count), nil, nil, 0)
}
