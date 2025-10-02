// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package seccomp

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/bpf"
	"gvisor.dev/gvisor/pkg/marshal/primitive"
	"subtrace.dev/cmd/run/fd"
	"subtrace.dev/cmd/run/kernel"
	"subtrace.dev/cmd/run/syscalls"
	"subtrace.dev/cmd/version"
)

const (
	SECCOMP_FILTER_FLAG_TSYNC       = (1 << 0) // ref: https://elixir.bootlin.com/linux/v6.1.112/source/include/uapi/linux/seccomp.h#L21
	SECCOMP_FILTER_FLAG_TSYNC_ESRCH = (1 << 4) // ref: https://elixir.bootlin.com/linux/v6.1.112/source/include/uapi/linux/seccomp.h#L25
)

var ErrCancelled = errors.New("seccomp user notification cancelled")

// InstallFilter installs a seccomp BPF program to filter the system calls we
// want to intercept. It return the file descriptor to be used with ioctl(2) to
// receive notifications.
//
// User notification-based seccomp filters are Linux 5.0+ only.
func InstallFilter(syscalls []int) (int, error) {
	const (
		// ref: https://github.com/google/gvisor/blob/3b57dd815f7fbe69b330410e8456633cfe209438/pkg/seccomp/seccomp_rules.go#LL31C1-L37C2
		offsetNR   = 0
		offsetArch = 4
		offsetArgs = 16
	)

	builder := bpf.NewProgramBuilder()

	// Check that the BPF program is running in the expected architecture. If
	// not, kill the process.
	builder.AddStmt(bpf.Ld|bpf.W|bpf.Abs, offsetArch)
	switch runtime.GOARCH {
	case "amd64":
		builder.AddJump(bpf.Jmp|bpf.Jeq|bpf.K, linux.AUDIT_ARCH_X86_64, 1, 0)
	case "arm64":
		builder.AddJump(bpf.Jmp|bpf.Jeq|bpf.K, linux.AUDIT_ARCH_AARCH64, 1, 0)
	default:
		return 0, fmt.Errorf("unsupported arch: %q", runtime.GOARCH)
	}
	builder.AddStmt(bpf.Ret|bpf.K, uint32(SECCOMP_RET_KILL_PROCESS))

	// Check if the number matches a syscall we're interested in intercepting.
	builder.AddStmt(bpf.Ld|bpf.W|bpf.Abs, offsetNR)
	for _, nr := range syscalls {
		builder.AddJump(bpf.Jmp|bpf.Jeq|bpf.K, uint32(nr), 0, 1)
		builder.AddStmt(bpf.Ret|bpf.K, SECCOMP_RET_USER_NOTIF)
	}

	// We're not interested in this syscall. Let the kernel handle it.
	builder.AddStmt(bpf.Ret|bpf.K, uint32(SECCOMP_RET_ALLOW))

	var instrs []linux.BPFInstruction
	if arr, err := builder.Instructions(); err != nil {
		return 0, fmt.Errorf("build: %w", err)
	} else {
		for _, ins := range arr {
			instrs = append(instrs, linux.BPFInstruction(ins))
		}
	}

	prog := &linux.SockFprog{
		Len:    uint16(len(instrs)),
		Filter: &instrs[0],
	}

	// Lock the goroutine's OS thread before installing seccomp filters because
	// without this the filter doesn't seem to apply to children. Not sure why.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// From seccomp(2) man page:
	//   In order to use the SECCOMP_SET_MODE_FILTER operation, either the
	//   calling thread must have the CAP_SYS_ADMIN capability in its user
	//   namespace, or the thread must already have the no_new_privs bit set.
	if !version.HasCapability(unix.CAP_SYS_ADMIN) {
		if _, _, errno := unix.Syscall(unix.SYS_PRCTL, linux.PR_SET_NO_NEW_PRIVS, 1, 0); errno != 0 {
			return 0, fmt.Errorf("prctl: PR_SET_NO_NEW_PRIVS: %w", errno)
		}
	}

	flags := uintptr(SECCOMP_FILTER_FLAG_NEW_LISTENER)

	// We just did a PR_SET_NO_NEW_PRIVS and we'll later be doing a execve(2) to
	// start the tracee process (see run.go). Let's say the PR_SET_NO_NEW_PRIVS=1
	// is done by thread A, the execve is done by thread B, and thread B got
	// created _before_ the prctl call. In this example, there's a race condition
	// between these two steps where the tracee gets started with no_new_privs=0
	// even though the prctl(PR_SET_NO_NEW_PRIVS, 1) returned success. By setting
	// SECCOMP_FILTER_FLAG_TSYNC, we tell the kernel to synchronize all threads
	// of the tracer to have the same seccomp settings in the seccomp(2) call
	// below that does SECCOMP_SET_MODE_FILTER.
	//
	// For historical reasons, the flags SECCOMP_FILTER_FLAG_TSYNC and
	// SECCOMP_FILTER_FLAG_NEW_LISTENER were declared to be mutually exclusive
	// even though there's no real reason why they should be so [1]. The flag
	// SECCOMP_FILTER_FLAG_TSYNC_ESRCH (introduced in Linux 5.7) allows using
	// both at the same time.
	//
	// [1] https://github.com/torvalds/linux/commit/51891498f2da78ee64dfad88fa53c9e85fb50abf
	flags |= uintptr(SECCOMP_FILTER_FLAG_TSYNC)
	flags |= uintptr(SECCOMP_FILTER_FLAG_TSYNC_ESRCH)

	// Setting SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV transitions the child
	// process into "wait killable semantics" after the notification has been
	// sent to the supervisor. This is useful when tracing programs written in
	// languages like Go: the Go scheduler will often interrupt long running
	// syscalls with SIGURG and this can result in a pointless retry loop. By
	// setting this flag, we tell the kernel to ignore non-fatal signals within
	// the child (small but measurable performance boost).
	//
	// This doesn't seem to be documented in the seccomp_unotify(2) manpage, but
	// it's Linux 5.19+ only, so check if the kernel version supports this
	// feature before setting this flag.
	//
	// [1] https://github.com/torvalds/linux/commit/c2aa2dfef243
	if _, _, err := kernel.CheckVersion("5.19", false); err == nil {
		slog.Debug("found kernel version 5.19+, enabling SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV")
		flags |= SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV
	}

	ret, _, errno := unix.Syscall(unix.SYS_SECCOMP, SECCOMP_SET_MODE_FILTER, flags, uintptr(unsafe.Pointer(prog)))
	if errno != 0 {
		return 0, fmt.Errorf("install: %w", errno)
	}
	return int(ret), nil
}

type Listener struct {
	fd *fd.FD
}

func NewFromFD(fd *fd.FD) *Listener {
	return &Listener{fd: fd}
}

func (l *Listener) Close() error {
	if !l.fd.ClosingIncRef() {
		return fmt.Errorf("already closed")
	}
	defer l.fd.DecRef()

	// Normally we'd do a l.fd.Lock() before calling close in order to safely
	// wait for all executing ioctl calls to finish before closing. However, as
	// seen in the seccomp_unotify(2) manpage's BUGS section (as of Linux 6.03):
	//
	//   If a SECCOMP_IOCTL_NOTIF_RECV ioctl(2) operation is performed after the
	//   target terminates, then the ioctl(2) call simply blocks (rather than
	//   returning an error to indicate that the target no longer exists).
	//
	// So attempting to Lock here would block forever, so we skip it. It's fine
	// in this case since the closing the seccomp listener means the subtrace
	// parent process is terminating anyway. If that changes, revisit.
	return unix.Close(l.fd.FD())
}

func (l *Listener) receiveSingle(b []byte) syscall.Errno {
	if !l.fd.IncRef() {
		return unix.EBADF
	}
	defer l.fd.DecRef()
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(l.fd.FD()), SECCOMP_IOCTL_NOTIF_RECV, uintptr(unsafe.Pointer(&b[0])))
	return errno
}

// Receive receives a single user notification.
func (l *Listener) Receive() (*Notif, syscall.Errno) {
	var x notif
	b := make([]byte, x.SizeBytes())
	for {
		errno := l.receiveSingle(b)
		if errno == 0 {
			break
		}
		if errno == unix.EINTR {
			continue
		}
		return nil, errno
	}
	x.UnmarshalBytes(b)

	if !l.fd.IncRef() {
		return nil, unix.EBADF
	}
	// Skipping defer DecRef here because we want to hold the ref until the
	// notification is marked as complete with Skip, Return, or AddFD.

	return &Notif{
		listener: l,
		ID:       uint64(x.id),
		PID:      int(x.pid),
		Syscall:  int(x.data.nr),
		Args: [6]uintptr{
			uintptr(x.data.args[0]),
			uintptr(x.data.args[1]),
			uintptr(x.data.args[2]),
			uintptr(x.data.args[3]),
			uintptr(x.data.args[4]),
			uintptr(x.data.args[5]),
		},
	}, 0
}

type Notif struct {
	listener *Listener
	state    atomic.Uint32

	ID      uint64     // seccomp notification ID
	PID     int        // tracee process PID
	Syscall int        // intercepted syscall number
	Args    [6]uintptr // intercepted syscall args
}

const (
	stateReceived = iota
	stateReplying
	stateReplied
	stateCancelled
	stateError
)

func (n *Notif) stateString() string {
	switch val := n.state.Load(); val {
	case stateReceived:
		return "Received"
	case stateReplying:
		return "Replying"
	case stateReplied:
		return "Replied"
	case stateCancelled:
		return "Cancelled"
	case stateError:
		return "Error"
	default:
		return fmt.Sprintf("Unknown_%d", val)
	}
}

func (n *Notif) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("pid", n.PID),
		slog.Group("syscall", "nr", n.Syscall, "name", syscalls.GetName(n.Syscall)),
		slog.String("id", fmt.Sprintf("0x%x", n.ID)),
		slog.String("fd", n.listener.fd.String()),
		slog.String("state", n.stateString()),
	)
}

func (n *Notif) String() string {
	return fmt.Sprintf("notif{id=0x%x,pid=%d,syscall=%s,state=%s}", n.ID, n.PID, syscalls.GetName(n.Syscall), n.stateString())
}

// Valid returns true if the notification is still valid.
func (n *Notif) Valid() bool {
	if !n.listener.fd.IncRef() {
		return false
	}
	defer n.listener.fd.DecRef()
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(n.listener.fd.FD()), SECCOMP_IOCTL_NOTIF_ID_VALID, uintptr(unsafe.Pointer(&n.ID)))
	return errno == 0
}

func (n *Notif) sendInner(handled bool, ret uintptr, errno syscall.Errno) error {
	// From seccomp_unotify(2) man page:
	//	 If flag contains SECCOMP_USER_NOTIF_FLAG_CONTINUE:
	//		 error and val fields must be zero
	//   If flag does not contain SECCOMP_USER_NOTIF_FLAG_CONTINUE:
	//     error is set either to 0 for a spoofed "success" return or to a
	//     negative error number for a spoofed "failure" return. In the former
	//     case, the kernel causes the target's system call to return the value
	//     specified in the val field. In the latter case, the kernel causes
	//     the target's system call to return -1, and errno is assigned the
	//     negated error value.
	var r resp
	r.id = primitive.Uint64(n.ID)
	if !handled {
		r.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE
	} else {
		r.val = primitive.Int64(ret)
		r.errno = primitive.Int32(-errno)
	}
	b := r.Bytes()
	_, _, sendErrno := unix.Syscall(unix.SYS_IOCTL, uintptr(n.listener.fd.FD()), SECCOMP_IOCTL_NOTIF_SEND, uintptr(unsafe.Pointer(&b[0])))
	switch sendErrno {
	case 0:
		n.state.CompareAndSwap(stateReplying, stateReplied)
		return nil
	case unix.ENOENT:
		n.state.CompareAndSwap(stateReplying, stateCancelled)
		return fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_SEND: %w", n, ErrCancelled)
	case unix.EINPROGRESS:
		if !n.Valid() {
			// ref: https://elixir.bootlin.com/linux/v6.4.7/source/kernel/seccomp.c#L1636
			// If the notification got invalidated between the find_notification call
			// and the knotif->state check, the kernel would return EINPROGRESS, not
			// ENOENT. This happens only when there's sufficient kernel load. During
			// testing, this never happened on real hardware, but it was triggered
			// often in a VM.
			n.state.CompareAndSwap(stateReplying, stateCancelled)
			return fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_SEND: %w", n, ErrCancelled)
		}
		return unix.EINPROGRESS
	default:
		n.state.CompareAndSwap(stateReplying, stateError)
		return fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_SEND: %w", n, errno)
	}
}

func (n *Notif) send(handled bool, ret uintptr, errno syscall.Errno) error {
	if !n.state.CompareAndSwap(stateReceived, stateReplying) {
		return unix.EALREADY
	}
	defer n.listener.fd.DecRef() // IncRef happened in SECCOMP_IOCTL_NOTIF_RECV

	return n.sendInner(handled, ret, errno)
}

// Skip tells the kernel handle that it needs to handle the intercepted syscall
// the way it normally would have.
func (n *Notif) Skip() error {
	return n.send(false, 0, 0)
}

// Return tells the kernel to set the given return value and errno for the
// intercepted syscall and mark the notification as complete. Use AddFD for
// syscalls that return a file/socket.
func (n *Notif) Return(ret uintptr, errno syscall.Errno) error {
	return n.send(true, ret, errno)
}

var SUBTRACE_SECCOMP_ADDFD_FLAGS primitive.Uint32 = SECCOMP_ADDFD_FLAG_SEND

func init() {
	if _, _, err := kernel.CheckVersion("5.14", true); err != nil {
		slog.Debug("kernel version < 5.14, unsetting SECCOMP_ADDFD_FLAG_SEND")
		SUBTRACE_SECCOMP_ADDFD_FLAGS ^= SECCOMP_ADDFD_FLAG_SEND
	}
}

// AddFD atomically installs a file descriptor and completes the notification.
// It returns the installed file descriptor's number in the tracee's file table
// if successful.
//
// Emulating syscalls that return a file descriptor (e.g. open, socket, accept)
// should use AddFD instead of Return(fd, 0). AddFD guarantees atomicity by
// using SECCOMP_ADDFD_FLAG_SEND internally instead of two separate ioctl calls
// for SECCOMP_IOCTL_NOTIF_ADDFD and SECCOMP_IOCTL_NOTIF_SEND. This is
// important because the seccomp notification may get cancelled between the two
// ioctl calls if the tracee chooses to interrupt its own syscall (Go programs
// do this a lot). This would result in a dangling file descriptor that's
// impossible to remove because doing so would require the tracee to call
// close(2) on a file descriptor that it doesn't even know exists, so the file
// descriptor table will quickly become full.
//
// By doing this atomically, the tracee is guaranteed to be aware of all file
// descriptors we install.
//
// The SECCOMP_ADDFD_FLAG_SEND flag is available in Linux 5.14+ only.
func (n *Notif) AddFD(fd *fd.FD, flags int) (int, error) {
	if !n.state.CompareAndSwap(stateReceived, stateReplying) {
		return 0, unix.EALREADY
	}
	defer n.listener.fd.DecRef() // IncRef happened in SECCOMP_IOCTL_NOTIF_RECV

	if !fd.IncRef() {
		return 0, unix.EBADF
	}
	defer fd.DecRef()

	var r addfd
	r.id = primitive.Uint64(n.ID)
	r.flags = SUBTRACE_SECCOMP_ADDFD_FLAGS
	r.srcFD = primitive.Uint32(fd.FD())
	r.newFDFlags = primitive.Uint32(flags)
	b := r.Bytes()
	target, _, addErrno := unix.Syscall(unix.SYS_IOCTL, uintptr(n.listener.fd.FD()), SECCOMP_IOCTL_NOTIF_ADDFD, uintptr(unsafe.Pointer(&b[0])))
	switch addErrno {
	case 0:
		if r.flags&SECCOMP_ADDFD_FLAG_SEND == 0 { // kernel < 5.14
			switch err := n.sendInner(true, target, 0); err {
			case nil:
				return int(target), nil
			default:
				return 0, fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_ADDFD: send after addfd: %w", n, err)
			}
		} else { // kernel >= 5.14
			n.state.CompareAndSwap(stateReplying, stateReplied)
			return int(target), nil
		}
	case unix.ENOENT:
		n.state.CompareAndSwap(stateReplying, stateCancelled)
		return 0, fmt.Errorf("%s: %w", n, ErrCancelled)
	case unix.EINPROGRESS:
		if !n.Valid() { // see send for more details
			n.state.CompareAndSwap(stateReplying, stateCancelled)
			return 0, fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_ADDFD: %w", n, ErrCancelled)
		}
		return 0, unix.EINPROGRESS
	default:
		n.state.CompareAndSwap(stateReplying, stateError)
		return 0, fmt.Errorf("%s: SECCOMP_IOCTL_NOTIF_ADDFD: %w", n, addErrno)
	}
}
