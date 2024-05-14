// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package process

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"syscall"

	"subtrace.dev/cli/cmd/run/engine/seccomp"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
)

var arch = binary.LittleEndian

// vmReadBytes reads at most maxSize bytes from the process's virtual memory
// starting at ptr. It returns an error if the address range isn't readable or
// if the notification is invalid.
func (p *Process) vmReadBytes(n *seccomp.Notif, ptr uintptr, maxSize int) ([]byte, syscall.Errno, error) {
	if ptr == 0 || maxSize == 0 {
		return nil, unix.EINVAL, nil
	}

	b := make([]byte, maxSize)
	local := []unix.Iovec{{Base: &b[0], Len: uint64(len(b))}}
	remote := []unix.RemoteIovec{{Base: ptr, Len: len(b)}}
	read, err := unix.ProcessVMReadv(int(p.PID), local, remote, 0)
	if err != nil {
		if !n.Valid() {
			return nil, 0, seccomp.ErrCancelled
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return nil, errno, nil
		}
		return nil, 0, fmt.Errorf("process_vm_readv: unknown error: %w", err)
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

	local := []unix.Iovec{{Base: &b[0], Len: uint64(len(b))}}
	remote := []unix.RemoteIovec{{Base: ptr, Len: len(b)}}
	wrote, err := unix.ProcessVMWritev(int(p.PID), local, remote, 0)
	if err != nil {
		if !n.Valid() {
			return 0, seccomp.ErrCancelled
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return errno, nil
		}
		return 0, fmt.Errorf("process_vm_writev: unknown error: %w", err)
	}
	if wrote < len(b) {
		if !n.Valid() {
			return 0, seccomp.ErrCancelled
		}
		return 0, fmt.Errorf("process_vm_writev: partial write: wrote %d, expected %d", wrote, len(b))
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
