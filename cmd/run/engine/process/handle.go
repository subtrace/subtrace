// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package process

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"subtrace.dev/cmd/run/engine/seccomp"
	"subtrace.dev/cmd/run/fd"
	"subtrace.dev/cmd/run/socket"
	"subtrace.dev/cmd/run/syscalls"
	"subtrace.dev/cmd/run/tls"
	"subtrace.dev/event"
)

// handleExit handles the exit(2) syscall.
func (p *Process) handleExit(n *seccomp.Notif, code int) error {
	if n.PID != p.PID { // we only care about the main thread exiting
		return n.Skip()
	}

	slog.Debug("process main thread is exiting", "proc", p, "code", code)
	if p.markAsExited() {
		go p.cleanup()
	}
	return n.Skip()
}

// handleExitGroup handles the exit_group(2) syscall.
func (p *Process) handleExitGroup(n *seccomp.Notif, code int) error {
	slog.Debug("process thread group is exiting", "proc", p, "code", code)
	if p.markAsExited() {
		go p.cleanup()
	}
	return n.Skip()
}

func (p *Process) handleExecve(n *seccomp.Notif, pathAddr uintptr, argvAddr uintptr, envpAddr uintptr) error {
	// Invalidate the event template cache so that sockets created by the new
	// program will require re-reading values for process event fields such as
	// process_exec_name, process_exec_size and process_cmdline.
	p.tmpl.Store(nil)
	return n.Skip()
}

func (p *Process) handleExecveat(n *seccomp.Notif, dirfd int, pathAddr uintptr, argvAddr uintptr, envpAddr uintptr, flags int) error {
	p.tmpl.Store(nil)
	return n.Skip()
}

func (p *Process) resolveDirfd(dirfd int) (string, syscall.Errno, error) {
	if dirfd == unix.AT_FDCWD {
		path, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", p.PID))
		if err != nil {
			return "", 0, fmt.Errorf("readlink cwd: %w", err)
		}
		return path, 0, nil
	}

	fd, errno := p.getFD(dirfd)
	if errno != 0 {
		return "", errno, nil
	}
	defer func() {
		if fd.ClosingIncRef() {
			defer fd.DecRef()
			fd.Lock()
			unix.Close(fd.FD())
		}
	}()
	defer fd.DecRef()

	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd.FD()))
	if err != nil {
		return "", 0, fmt.Errorf("readlink dirfd: %w", err)
	}
	return path, 0, nil
}

func (p *Process) resolvePath(n *seccomp.Notif, dirfd int, pathAddr uintptr) (string, syscall.Errno, error) {
	path, errno, err := p.vmReadString(n, pathAddr, unix.PathMax)
	if errno != 0 || err != nil {
		return "", errno, err
	}

	if !filepath.IsAbs(path) {
		dirpath, errno, err := p.resolveDirfd(dirfd)
		if errno != 0 || err != nil {
			return "", errno, err
		}
		path = filepath.Join(dirpath, path)
	}

	return path, 0, nil
}

// handleOpen handles the open(2) and openat(2) syscalls. We do this in order
// to inject the ephemeral CA certificate into the system root CA store so that
// we can intercept outgoing TLS requests later.
func (p *Process) handleOpen(n *seccomp.Notif, dirfd int, pathAddr uintptr, flags int, mode int) error {
	if flags&unix.O_WRONLY != 0 {
		return n.Skip()
	}

	path, errno, err := p.resolvePath(n, dirfd, pathAddr)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}
	if !tls.Enabled || !tls.IsKnownPath(path) {
		return n.Skip()
	}

	slog.Debug("handling TLS CA cert file open", "path", path)

	ephemeralPEM := tls.GetEphemeralCAPEM()

	orig, err := os.ReadFile(path)
	if err != nil {
		return n.Skip()
	}

	ret, err := unix.MemfdCreate("subtrace_override:"+path, unix.FD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("inject ephemeral CA: memfd_create: %w", err)
	}

	memfd := fd.NewFD(ret)
	defer memfd.DecRef()
	defer unix.Close(memfd.FD())

	if _, err := unix.Write(memfd.FD(), append(orig, ephemeralPEM...)); err != nil {
		return fmt.Errorf("inject ephemeral CA: write: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LSEEK, uintptr(memfd.FD()), 0, 0); errno != 0 {
		return fmt.Errorf("inject ephemeral CA: lseek: %w", err)
	}

	if _, err := n.AddFD(memfd, flags); err != nil { // we don't care about the fd value
		return fmt.Errorf("addfd: %w", err)
	}
	return nil
}

// handleFstatat handles the fstatat(2) syscall. We intercept it to ensure that
// the file size of the system root CA store is consistent with the ephemeral
// CA injection we do in the open(2) handler.
//
// Note that we don't handle fstat(2) because it directly operates on an open
// file descriptor.
//
// TODO: stat(2)
func (p *Process) handleFstatat(n *seccomp.Notif, dirfd int, pathAddr uintptr, bufAddr uintptr, flags int) error {
	path, errno, err := p.resolvePath(n, dirfd, pathAddr)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}
	if !tls.Enabled || !tls.IsKnownPath(path) {
		return n.Skip()
	}

	ephemeralPEM := tls.GetEphemeralCAPEM()

	var nr int
	switch runtime.GOARCH {
	case "amd64":
		nr = syscalls.GetNumber("SYS_NEWFSTATAT")
	case "arm64":
		nr = syscalls.GetNumber("SYS_FSTATAT")
	default:
		panic(fmt.Sprintf("GOARCH=%s: unsupported", runtime.GOARCH))
	}

	var orig linux.Stat
	b := make([]byte, orig.SizeBytes(), orig.SizeBytes())
	if _, _, errno := unix.Syscall6(uintptr(nr), uintptr(dirfd), pathAddr, uintptr(unsafe.Pointer(&b[0])), uintptr(flags), 0, 0); errno != 0 {
		return n.Skip()
	}
	orig.UnmarshalBytes(b)

	repl := orig
	repl.Size += int64(len(ephemeralPEM))
	b = make([]byte, repl.SizeBytes(), repl.SizeBytes())
	repl.MarshalBytes(b)

	errno, err = p.vmWriteBytes(n, bufAddr, b)
	if err != nil {
		return fmt.Errorf("write stat to process memory: %w", err)
	}
	return n.Return(0, errno)
}

// handleStatx handles the statx(2) syscall.
func (p *Process) handleStatx(n *seccomp.Notif, dirfd int, pathAddr uintptr, flags int, mask int, bufAddr uintptr) error {
	path, errno, err := p.resolvePath(n, dirfd, pathAddr)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}
	if !tls.Enabled || !tls.IsKnownPath(path) {
		return n.Skip()
	}

	ephemeralPEM := tls.GetEphemeralCAPEM()

	pathb := []byte(path)

	var orig linux.Statx
	b := make([]byte, orig.SizeBytes(), orig.SizeBytes())
	if _, _, errno := unix.Syscall6(unix.SYS_STATX, uintptr(dirfd), uintptr(unsafe.Pointer(&pathb[0])), uintptr(flags), uintptr(mask), uintptr(unsafe.Pointer(&b[0])), 0); errno != 0 {
		return n.Skip()
	}
	orig.UnmarshalBytes(b)

	repl := orig
	repl.Size += uint64(len(ephemeralPEM))
	b = make([]byte, repl.SizeBytes(), repl.SizeBytes())
	repl.MarshalBytes(b)

	errno, err = p.vmWriteBytes(n, bufAddr, b)
	if err != nil {
		return fmt.Errorf("write statx to process memory: %w", err)
	}
	return n.Return(0, errno)
}

// handleClose handles the close(2) syscall.
func (p *Process) handleClose(n *seccomp.Notif, fd int) error {
	s, ok := p.getDeleteSocket(fd)
	if !ok {
		return n.Skip()
	}

	if errno := s.Close(); errno != 0 {
		// Failing to close a socket cleanly isn't a fatal error. Moreover, we need
		// to let the kernel handle the syscall regardless of whether our close
		// fails because the socket needs to be removed from the process' file
		// descriptor table.
		slog.Debug("failed to close socket cleanly", "errno", errno)
		return n.Skip()
	}

	// Allow the syscall to proceed so that the process can close its copy of the
	// socket from its file descriptor table.
	return n.Skip()
}

// handleSocket handles the socket(2) syscall.
func (p *Process) handleSocket(n *seccomp.Notif, domain, typ, protocol int) error {
	// TODO(adtac): support connect(AF_UNSPEC) on tracked sockets as a way to
	// dissolve connection state (see connect(2) manpage).
	if domain != unix.AF_INET && domain != unix.AF_INET6 {
		return n.Skip()
	}
	if typ&unix.SOCK_STREAM == 0 {
		return n.Skip()
	}
	if protocol == unix.IPPROTO_IP {
		protocol = unix.IPPROTO_TCP // see /usr/include/linux/in.h
	}
	if protocol != unix.IPPROTO_TCP {
		return n.Skip()
	}

	sock, err := socket.NewSocket(p.devtools, event.NewFromTemplate(p.getEventTemplate()), domain, typ, p.config)
	if err != nil {
		return fmt.Errorf("create new socket: %w", err)
	}
	if err := p.installSocket(n, sock, typ&unix.SOCK_CLOEXEC); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	return nil
}

// handleConnect handles the bind(2) syscall.
func (p *Process) handleBind(n *seccomp.Notif, fd int, addrPtr uintptr, addrSize int) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	bind, errno, err := p.vmReadSockaddr(n, addrPtr, addrSize)
	if err != nil {
		return fmt.Errorf("read bind addr: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}

	errno, err = s.Bind(bind)
	if err != nil {
		return fmt.Errorf("bind socket: %w", err)
	}
	return n.Return(0, errno)
}

// handleConnect handles the connect(2) syscall.
func (p *Process) handleConnect(n *seccomp.Notif, fd int, addrPtr uintptr, addrSize int) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	peer, errno, err := p.vmReadSockaddr(n, addrPtr, addrSize)
	if err != nil {
		return fmt.Errorf("read peer addr: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}

	errno, err = s.Connect(peer)
	if err != nil {
		return fmt.Errorf("connect socket: %w", err)
	}
	return n.Return(0, errno)
}

// handleListen handles the listen(2) syscall.
func (p *Process) handleListen(n *seccomp.Notif, fd int, backlog int) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	errno, err := s.Listen(backlog)
	if err != nil {
		return fmt.Errorf("mark socket as listening: %w", err)
	}
	return n.Return(0, errno)
}

// handleAccept handles the accept(2) and accept4(2) syscalls.
func (p *Process) handleAccept(n *seccomp.Notif, fd int, addrPtr uintptr, addrSizePtr uintptr, flags int) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	ret, errno, err := s.Accept(flags)
	if err != nil {
		return fmt.Errorf("accept socket: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}

	if addrPtr != 0 && addrSizePtr != 0 {
		peer, errno, err := ret.PeerAddr()
		if err != nil {
			return fmt.Errorf("get peer addr of accepted socket: %w", err)
		}
		if errno != 0 {
			return n.Return(0, errno)
		}

		if s.Domain == unix.AF_INET6 && peer.Addr().Is4() {
			// `python -m http.server -b ::` followed by `curl -4 localhost:8000`
			// reports the client address as ::ffff:127.0.0.1, not the IPv4 address.
			peer = netip.AddrPortFrom(netip.AddrFrom16(peer.Addr().As16()), peer.Port())
		}

		errno, err = p.vmWriteSockaddr(n, peer, addrPtr, addrSizePtr)
		if err != nil {
			return fmt.Errorf("write sock addr: %w", err)
		}
		if errno != 0 {
			// TODO(adtac): gvisor [0] says Linux doesn't give you an error here, but
			// it looks like the kernel source code does [1]. Check who is right here.
			// [0] https://github.com/google/gvisor/blob/7b151e25d076b81480456069917baffc2808578f/pkg/sentry/syscalls/linux/sys_socket.go#L313
			// [1] https://elixir.bootlin.com/linux/v6.7/source/net/socket.c#L1942
			return n.Return(0, errno)
		}
	}

	if err := p.installSocket(n, ret, flags&unix.SOCK_CLOEXEC); err != nil {
		return fmt.Errorf("install socket: %w", err)
	}
	return nil
}

// handleGetsockopt handles the getsockopt(2) syscall to emulate SO_ERROR.
func (p *Process) handleGetsockopt(n *seccomp.Notif, fd int, level int, name int, valPtr uintptr, valSizePtr uintptr) error {
	if level != unix.SOL_SOCKET || name != unix.SO_ERROR {
		return n.Skip()
	}

	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	valSize, errno, err := p.vmReadUint32(n, valSizePtr)
	if err != nil {
		return fmt.Errorf("read value size pointer: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}
	if valSize != 4 {
		return n.Return(0, unix.EINVAL)
	}

	errno, err = p.vmWriteUint32(n, valPtr, uint32(s.Errno()))
	if err != nil {
		return fmt.Errorf("write value: %w", err)
	}
	return n.Return(0, errno)
}

// handleGetsockname handles the getsockname(2) syscall to emulate the external
// connection's bind address.
func (p *Process) handleGetsockname(n *seccomp.Notif, fd int, addrPtr uintptr, addrSizePtr uintptr) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	bind, errno, err := s.BindAddr()
	if err != nil {
		return fmt.Errorf("get bind addr: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}

	if !bind.IsValid() {
		errno, err := p.vmWriteUint32(n, addrSizePtr, 0)
		if err != nil {
			return fmt.Errorf("write zero size: %w", err)
		}
		if errno != 0 {
			return n.Return(0, errno)
		}
		return n.Return(0, 0)
	}

	if s.Domain == unix.AF_INET6 && bind.Addr().Is4() {
		bind = netip.AddrPortFrom(netip.AddrFrom16(bind.Addr().As16()), bind.Port())
	}

	if addrPtr == 0 || addrSizePtr == 0 {
		return n.Return(0, unix.EFAULT)
	}
	errno, err = p.vmWriteSockaddr(n, bind, addrPtr, addrSizePtr)
	if err != nil {
		return fmt.Errorf("write bind addr: %w", err)
	}
	return n.Return(0, errno)
}

// handleGetpeername handles the getpeername(2) syscall to emulate the external
// connection's peer address.
func (p *Process) handleGetpeername(n *seccomp.Notif, fd int, addrPtr uintptr, addrSizePtr uintptr) error {
	s, ok := p.getSocket(fd)
	if !ok {
		return n.Skip()
	}

	peer, errno, err := s.PeerAddr()
	if err != nil {
		return fmt.Errorf("get peer addr: %w", err)
	}
	if errno != 0 {
		return n.Return(0, errno)
	}

	if !peer.IsValid() {
		return n.Return(0, unix.ENOTCONN)
	}

	if s.Domain == unix.AF_INET6 && peer.Addr().Is4() {
		peer = netip.AddrPortFrom(netip.AddrFrom16(peer.Addr().As16()), peer.Port())
	}

	if addrPtr == 0 || addrSizePtr == 0 {
		return n.Return(0, unix.EFAULT)
	}
	errno, err = p.vmWriteSockaddr(n, peer, addrPtr, addrSizePtr)
	if err != nil {
		return fmt.Errorf("write peer addr: %w", err)
	}
	return n.Return(0, errno)
}

var Handlers [1024]func(*Process, *seccomp.Notif) error

func init() {
	Handlers[unix.SYS_EXIT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleExit(n, int(n.Args[0]))
	}
	Handlers[unix.SYS_EXIT_GROUP] = func(p *Process, n *seccomp.Notif) error {
		return p.handleExitGroup(n, int(n.Args[0]))
	}

	Handlers[unix.SYS_EXECVE] = func(p *Process, n *seccomp.Notif) error {
		return p.handleExecve(n, uintptr(n.Args[0]), uintptr(n.Args[1]), uintptr(n.Args[2]))
	}
	Handlers[unix.SYS_EXECVEAT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleExecveat(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]), uintptr(n.Args[3]), int(n.Args[4]))
	}

	Handlers[unix.SYS_OPENAT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleOpen(n, int(int32(n.Args[0])), uintptr(n.Args[1]), int(n.Args[2]), int(n.Args[3]))
	}

	switch runtime.GOARCH {
	case "amd64":
		Handlers[syscalls.GetNumber("SYS_NEWFSTATAT")] = func(p *Process, n *seccomp.Notif) error {
			return p.handleFstatat(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]), int(n.Args[3]))
		}
	case "arm64":
		Handlers[syscalls.GetNumber("SYS_FSTATAT")] = func(p *Process, n *seccomp.Notif) error {
			return p.handleFstatat(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]), int(n.Args[3]))
		}
	default:
		panic(fmt.Sprintf("GOARCH=%s: unsupported", runtime.GOARCH))
	}

	Handlers[unix.SYS_STATX] = func(p *Process, n *seccomp.Notif) error {
		return p.handleStatx(n, int(int32(n.Args[0])), uintptr(n.Args[1]), int(n.Args[2]), int(n.Args[3]), uintptr(n.Args[4]))
	}

	Handlers[unix.SYS_CLOSE] = func(p *Process, n *seccomp.Notif) error {
		return p.handleClose(n, int(int32(n.Args[0])))
	}

	Handlers[unix.SYS_SOCKET] = func(p *Process, n *seccomp.Notif) error {
		return p.handleSocket(n, int(n.Args[0]), int(n.Args[1]), int(n.Args[2]))
	}

	Handlers[unix.SYS_BIND] = func(p *Process, n *seccomp.Notif) error {
		return p.handleBind(n, int(int32(n.Args[0])), uintptr(n.Args[1]), int(n.Args[2]))
	}

	Handlers[unix.SYS_CONNECT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleConnect(n, int(int32(n.Args[0])), uintptr(n.Args[1]), int(n.Args[2]))
	}

	Handlers[unix.SYS_LISTEN] = func(p *Process, n *seccomp.Notif) error {
		return p.handleListen(n, int(int32(n.Args[0])), int(n.Args[1]))
	}

	Handlers[unix.SYS_ACCEPT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleAccept(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]), 0)
	}
	Handlers[unix.SYS_ACCEPT4] = func(p *Process, n *seccomp.Notif) error {
		return p.handleAccept(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]), int(n.Args[3]))
	}

	Handlers[unix.SYS_GETSOCKOPT] = func(p *Process, n *seccomp.Notif) error {
		return p.handleGetsockopt(n, int(int32(n.Args[0])), int(n.Args[1]), int(n.Args[2]), uintptr(n.Args[3]), uintptr(n.Args[4]))
	}

	Handlers[unix.SYS_GETSOCKNAME] = func(p *Process, n *seccomp.Notif) error {
		return p.handleGetsockname(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]))
	}

	Handlers[unix.SYS_GETPEERNAME] = func(p *Process, n *seccomp.Notif) error {
		return p.handleGetpeername(n, int(int32(n.Args[0])), uintptr(n.Args[1]), uintptr(n.Args[2]))
	}
}
