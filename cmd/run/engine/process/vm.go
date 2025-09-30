// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package process

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"subtrace.dev/cmd/run/engine/seccomp"
)

var arch = binary.LittleEndian

// Functions for reading and writing memory inside the tracee process. We
// dynamically select between using process_vm_readv/writev (preferred) or
// falling back to /proc/pid/mem based on what's available at runtime.
var readMemory func(pid int, buf []byte, addr uintptr) (int, error)
var writeMemory func(pid int, buf []byte, addr uintptr) (int, error)

func InitReadWriteVM() {
	tmp := make([]byte, 1)
	local := []unix.Iovec{{Base: &tmp[0], Len: 1}}
	remote := []unix.RemoteIovec{{Base: uintptr(unsafe.Pointer(&tmp[0])), Len: 1}}
	switch _, err := unix.ProcessVMReadv(os.Getpid(), local, remote, 0); err {
	case nil:
		readMemory = func(pid int, buf []byte, addr uintptr) (int, error) {
			local := []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}}
			remote := []unix.RemoteIovec{{Base: addr, Len: len(buf)}}
			return unix.ProcessVMReadv(pid, local, remote, 0)
		}
		writeMemory = func(pid int, buf []byte, addr uintptr) (int, error) {
			local := []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}}
			remote := []unix.RemoteIovec{{Base: addr, Len: len(buf)}}
			return unix.ProcessVMWritev(pid, local, remote, 0)
		}

	case syscall.ENOSYS:
		slog.Debug("process_vm_readv/process_vm_writev unavailable, falling back to /proc/pid/mem approach for tracee memory read and write")
		readMemory = func(pid int, buf []byte, addr uintptr) (int, error) {
			fd, err := unix.Open(fmt.Sprintf("/proc/%d/mem", pid), os.O_RDWR, 0o644)
			if err != nil {
				return 0, fmt.Errorf("open mem: %w", err)
			}
			defer unix.Close(fd)
			return unix.Preadv(fd, [][]byte{buf}, int64(addr))
		}
		writeMemory = func(pid int, buf []byte, addr uintptr) (int, error) {
			fd, err := unix.Open(fmt.Sprintf("/proc/%d/mem", pid), os.O_RDWR, 0o644)
			if err != nil {
				return 0, fmt.Errorf("open mem: %w", err)
			}
			defer unix.Close(fd)
			return unix.Pwritev(fd, [][]byte{buf}, int64(addr))
		}

	default:
		panic(fmt.Sprintf("failed to test process_vm_readv on self: %v", err))
	}
}

// vmReadBytes reads at most maxSize bytes from the process's virtual memory
// starting at ptr. It returns an error if the address range isn't readable or
// if the notification is invalid.
func (p *Process) vmReadBytes(n *seccomp.Notif, ptr uintptr, maxSize int) ([]byte, syscall.Errno, error) {
	if maxSize == 0 {
		return []byte{}, 0, nil
	}
	if ptr == 0 {
		return nil, unix.EINVAL, nil
	}

	b := make([]byte, maxSize)
	read, err := readMemory(int(p.PID), b, ptr)
	if err != nil {
		if !n.Valid() {
			return nil, 0, seccomp.ErrCancelled
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return nil, errno, nil
		}
		return nil, 0, fmt.Errorf("memory read: unknown error: %w", err)
	}
	if !n.Valid() {
		return nil, 0, seccomp.ErrCancelled
	}
	return b[:read], 0, nil
}

// vmReadUint32 reads a uint32 from the process' memory starting at ptr.
func (p *Process) vmReadUint32(n *seccomp.Notif, ptr uintptr) (uint32, syscall.Errno, error) {
	b, errno, err := p.vmReadBytes(n, ptr, 4)
	if errno != 0 || err != nil {
		return 0, errno, err
	}
	if len(b) < 4 {
		return 0, unix.EINVAL, nil
	}
	return arch.Uint32(b), 0, nil
}

// vmReadString reads a NULL-terminated string from the process virtual memory
// starting at ptr.
func (p *Process) vmReadString(n *seccomp.Notif, ptr uintptr, maxSize int) (string, syscall.Errno, error) {
	if ptr == 0 {
		return "", unix.EINVAL, nil
	}

	b, errno, err := p.vmReadBytes(n, ptr, maxSize)
	if errno != 0 || err != nil {
		return "", errno, err
	}
	if idx := bytes.IndexByte(b, 0); idx != -1 {
		b = b[:idx]
	}
	return string(b), 0, nil
}

// vmReadSockaddr reads a sockaddr struct of size bytes to the process's
// virtual memory starting at ptr.
func (p *Process) vmReadSockaddr(n *seccomp.Notif, ptr uintptr, size int) (netip.AddrPort, syscall.Errno, error) {
	if ptr == 0 || size == 0 {
		return netip.AddrPort{}, unix.EINVAL, nil
	}

	b, errno, err := p.vmReadBytes(n, ptr, size)
	if errno != 0 || err != nil {
		return netip.AddrPort{}, errno, err
	}
	if len(b) < 2 {
		return netip.AddrPort{}, unix.EINVAL, nil
	}

	family := arch.Uint16(b[0:2])
	switch family {
	case unix.AF_INET:
		sa := new(linux.SockAddrInet)
		if len(b) < sa.SizeBytes() {
			return netip.AddrPort{}, unix.EINVAL, nil
		}
		sa.UnmarshalBytes(b)
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), htons(sa.Port)), 0, nil

	case unix.AF_INET6:
		sa := new(linux.SockAddrInet6)
		if len(b) < sa.SizeBytes() {
			return netip.AddrPort{}, unix.EINVAL, nil
		}
		sa.UnmarshalBytes(b)
		return netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), htons(sa.Port)), 0, nil

	default:
		return netip.AddrPort{}, unix.EINVAL, nil
	}
}

// vmWriteBytes writes b to the process's memory starting at ptr. It returns an
// error if the address range isn't writable or if the notification is invalid.
func (p *Process) vmWriteBytes(n *seccomp.Notif, ptr uintptr, b []byte) (syscall.Errno, error) {
	if !n.Valid() {
		return 0, seccomp.ErrCancelled
	}

	if len(b) == 0 {
		return 0, nil
	}
	if ptr == 0 {
		return unix.EINVAL, nil
	}

	wrote, err := writeMemory(int(p.PID), b, ptr)
	if err != nil {
		if !n.Valid() {
			return 0, seccomp.ErrCancelled
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return errno, nil
		}
		return 0, fmt.Errorf("memory write: unknown error: %w", err)
	}
	if wrote < len(b) {
		if !n.Valid() {
			return 0, seccomp.ErrCancelled
		}
		return 0, fmt.Errorf("memory write: partial write: wrote %d, expected %d", wrote, len(b))
	}
	return 0, nil
}

// vmWriteInt32 writes a uint32 to the process' memory at ptr.
func (p *Process) vmWriteUint32(n *seccomp.Notif, ptr uintptr, val uint32) (syscall.Errno, error) {
	return p.vmWriteBytes(n, ptr, arch.AppendUint32(nil, val))
}

// vmWriteSockaddr writes a sockaddr struct and its length to the process'
// memory at ptr. It panics if addr is invalid or if ptr/sizePtr is NULL.
func (p *Process) vmWriteSockaddr(n *seccomp.Notif, addr netip.AddrPort, ptr uintptr, sizePtr uintptr) (syscall.Errno, error) {
	if !addr.IsValid() {
		panic("invalid AddrPort")
	}
	if ptr == 0 || sizePtr == 0 {
		panic(fmt.Sprintf("NULL: ptr=%x, sizePtr=%x", ptr, sizePtr))
	}

	avail, errno, err := p.vmReadUint32(n, sizePtr)
	if errno != 0 || err != nil {
		return errno, err
	}
	if avail < 0 {
		return unix.EINVAL, nil
	}

	var sa linux.SockAddr
	switch {
	case addr.Addr().Is6() || addr.Addr().Is4In6():
		sa = &linux.SockAddrInet6{
			Family: unix.AF_INET6,
			Addr:   linux.Inet6Addr(addr.Addr().As16()),
			Port:   htons(addr.Port()),
		}
	case addr.Addr().Is4():
		sa = &linux.SockAddrInet{
			Family: unix.AF_INET,
			Addr:   linux.InetAddr(addr.Addr().As4()),
			Port:   htons(addr.Port()),
		}
	}

	b := make([]byte, sa.SizeBytes(), sa.SizeBytes())
	sa.MarshalBytes(b)
	if uint32(sa.SizeBytes()) > avail {
		b = b[:avail]
	}

	if errno, err := p.vmWriteBytes(n, ptr, b); errno != 0 || err != nil {
		return errno, err
	}
	if errno, err := p.vmWriteUint32(n, sizePtr, uint32(sa.SizeBytes())); errno != 0 || err != nil {
		return errno, err
	}
	return 0, nil
}
