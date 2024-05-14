// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package seccomp

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/marshal/primitive"
)

const (
	// ref: <linux/seccomp.h>: https://elixir.bootlin.com/linux/v6.7/source/include/uapi/linux/seccomp.h
	SECCOMP_SET_MODE_FILTER                = 1
	SECCOMP_RET_KILL_PROCESS               = 0x80000000
	SECCOMP_RET_KILL_THREAD                = 0x00000000
	SECCOMP_RET_KILL                       = SECCOMP_RET_KILL_THREAD
	SECCOMP_RET_TRAP                       = 0x00030000
	SECCOMP_RET_ERRNO                      = 0x00050000
	SECCOMP_RET_USER_NOTIF                 = 0x7fc00000
	SECCOMP_RET_TRACE                      = 0x7ff00000
	SECCOMP_RET_LOG                        = 0x7ffc0000
	SECCOMP_RET_ALLOW                      = 0x7fff0000
	SECCOMP_USER_NOTIF_FLAG_CONTINUE       = 1 << 0
	SECCOMP_FILTER_FLAG_NEW_LISTENER       = 1 << 3
	SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV = 1 << 5
	SECCOMP_ADDFD_FLAG_SETFD               = 1 << 0
	SECCOMP_ADDFD_FLAG_SEND                = 1 << 1

	// NOTE: these values are not explicitly defined in seccomp.h and they had to
	// be found with a short C program.
	SECCOMP_IOCTL_NOTIF_RECV     = 0xc0502100
	SECCOMP_IOCTL_NOTIF_SEND     = 0xc0182101
	SECCOMP_IOCTL_NOTIF_ADDFD    = 0x40182103
	SECCOMP_IOCTL_NOTIF_ID_VALID = 0x40082102
)

// ref: <linux/seccomp.h>: struct seccomp_notif: https://elixir.bootlin.com/linux/v6.7/source/include/uapi/linux/seccomp.h#L75
type notif struct {
	id    primitive.Uint64
	pid   primitive.Uint32
	flags primitive.Uint32
	data  struct {
		nr   primitive.Int32
		arch primitive.Uint32
		ip   primitive.Uint64
		args [6]primitive.Uint64
	}
}

func (x *notif) SizeBytes() int {
	var n int
	n += x.id.SizeBytes()
	n += x.pid.SizeBytes()
	n += x.flags.SizeBytes()
	n += x.data.nr.SizeBytes()
	n += x.data.arch.SizeBytes()
	n += x.data.ip.SizeBytes()
	n += x.data.args[0].SizeBytes()
	n += x.data.args[1].SizeBytes()
	n += x.data.args[2].SizeBytes()
	n += x.data.args[3].SizeBytes()
	n += x.data.args[4].SizeBytes()
	n += x.data.args[5].SizeBytes()
	return n
}

func (x *notif) UnmarshalBytes(b []byte) []byte {
	if len(b) != x.SizeBytes() {
		panic(fmt.Sprintf("got len(b)=%d, want %d bytes", len(b), x.SizeBytes()))
	}
	b = x.id.UnmarshalBytes(b)
	b = x.pid.UnmarshalBytes(b)
	b = x.flags.UnmarshalBytes(b)
	b = x.data.nr.UnmarshalBytes(b)
	b = x.data.arch.UnmarshalBytes(b)
	b = x.data.ip.UnmarshalBytes(b)
	b = x.data.args[0].UnmarshalBytes(b)
	b = x.data.args[1].UnmarshalBytes(b)
	b = x.data.args[2].UnmarshalBytes(b)
	b = x.data.args[3].UnmarshalBytes(b)
	b = x.data.args[4].UnmarshalBytes(b)
	b = x.data.args[5].UnmarshalBytes(b)
	return b
}

// ref: <linux/seccomp.h>: struct seccomp_notif_resp: https://elixir.bootlin.com/linux/v6.7/source/include/uapi/linux/seccomp.h#L111
type resp struct {
	id    primitive.Uint64
	val   primitive.Int64
	errno primitive.Int32
	flags primitive.Uint32
}

func (x *resp) SizeBytes() int {
	var n int
	n += x.id.SizeBytes()
	n += x.val.SizeBytes()
	n += x.errno.SizeBytes()
	n += x.flags.SizeBytes()
	return n
}

func (x *resp) Bytes() []byte {
	ret := make([]byte, x.SizeBytes())
	b := ret
	b = x.id.MarshalBytes(b)
	b = x.val.MarshalBytes(b)
	b = x.errno.MarshalBytes(b)
	b = x.flags.MarshalBytes(b)
	return ret
}

// ref: <linux/seccomp.h>: struct seccomp_notif_addfd: https://elixir.bootlin.com/linux/v6.7/source/include/uapi/linux/seccomp.h#L132
type addfd struct {
	id         primitive.Uint64
	flags      primitive.Uint32
	srcFD      primitive.Uint32
	newFD      primitive.Uint32
	newFDFlags primitive.Uint32
}

func (x *addfd) SizeBytes() int {
	var n int
	n += x.id.SizeBytes()
	n += x.flags.SizeBytes()
	n += x.srcFD.SizeBytes()
	n += x.newFD.SizeBytes()
	n += x.newFD.SizeBytes()
	return n
}

func (x *addfd) Bytes() []byte {
	ret := make([]byte, x.SizeBytes())
	b := ret
	b = x.id.MarshalBytes(b)
	b = x.flags.MarshalBytes(b)
	b = x.srcFD.MarshalBytes(b)
	b = x.newFD.MarshalBytes(b)
	b = x.newFDFlags.MarshalBytes(b)
	return ret
}
