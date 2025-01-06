// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package socket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/config"
	"subtrace.dev/cmd/run/fd"
	"subtrace.dev/devtools"
	"subtrace.dev/event"
)

const (
	StatusPassive = iota
	StatusConnected
	StatusConnecting
	StatusListening
	StatusClosed
)

const (
	InitiatorUndefined = iota
	InitiatorProcess
	InitiatorExternal
)

type immutable struct {
	status int

	passive struct {
		bind  *fd.FD
		errno syscall.Errno
	}

	connecting struct {
		bind *fd.FD
		peer netip.AddrPort
	}

	connected struct {
		proxy *proxy
	}

	listening struct {
		active  atomic.Bool
		lis     net.Listener
		backlog sync.Map
	}
}

func getsockname(s *fd.FD) (netip.AddrPort, syscall.Errno, error) {
	if !s.IncRef() {
		return netip.AddrPort{}, unix.EBADF, nil
	}
	defer s.DecRef()

	sa, err := unix.Getsockname(s.FD())
	if err != nil {
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			return netip.AddrPort{}, 0, fmt.Errorf("getsockname: %w", err)
		}
		return netip.AddrPort{}, errno, nil
	}

	switch sa := sa.(type) {
	case *unix.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port)), 0, nil
	case *unix.SockaddrInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), uint16(sa.Port)), 0, nil
	}
	panic("unreachable")
}

func (imm *immutable) getRemoteBindAddr() (netip.AddrPort, syscall.Errno, error) {
	switch imm.status {
	case StatusPassive:
		if imm.passive.bind == nil {
			return netip.AddrPort{}, 0, nil
		}
		return getsockname(imm.passive.bind)

	case StatusConnected:
		addr, err := netip.ParseAddrPort(imm.connected.proxy.external.LocalAddr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("connected: parse addr: %w", err)
		}
		return addr, 0, nil

	case StatusConnecting:
		if imm.connecting.bind == nil {
			panic("connecting socket has no bind")
		}
		return getsockname(imm.connecting.bind)

	case StatusListening:
		addr, err := netip.ParseAddrPort(imm.listening.lis.Addr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("listen: parse addr: %w", err)
		}
		return addr, 0, nil

	case StatusClosed:
		return netip.AddrPort{}, unix.EBADF, nil
	}
	panic("unreachable")
}

func (imm *immutable) getRemotePeerAddr() (netip.AddrPort, syscall.Errno, error) {
	switch imm.status {
	case StatusPassive:
		return netip.AddrPort{}, 0, nil

	case StatusConnected:
		addr, err := netip.ParseAddrPort(imm.connected.proxy.external.RemoteAddr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("external conn: parse remote addr: %w", err)
		}
		return addr, 0, nil

	case StatusConnecting:
		return imm.connecting.peer, 0, nil

	case StatusListening:
		return netip.AddrPort{}, unix.EINVAL, nil

	case StatusClosed:
		return netip.AddrPort{}, unix.EBADF, nil
	}
	panic("unreachable")
}

type Socket struct {
	Domain int
	FD     *fd.FD

	current atomic.Pointer[immutable]

	devtools *devtools.Server
	tmpl     *event.Event

	config *config.Config
}

func NewSocket(devtools *devtools.Server, tmpl *event.Event, domain int, typ int, config *config.Config) (*Socket, error) {
	if domain != unix.AF_INET && domain != unix.AF_INET6 {
		return nil, fmt.Errorf("unsupported domain 0x%x", domain)
	}

	// Explicitly add SOCK_CLOEXEC because even if the target process didn't ask
	// for it, this socket will be in our file descriptor table. When the engine
	// installs the socket into the target's file descriptor table, the correct
	// CLOEXEC flag will be set so that the target's expectation is satisfied.
	typ |= unix.SOCK_CLOEXEC

	ret, err := unix.Socket(domain, typ, unix.IPPROTO_TCP)
	if err != nil {
		return nil, fmt.Errorf("socket syscall: %w", err)
	}
	fd := fd.NewFD(ret)
	defer fd.DecRef()

	s := &Socket{Domain: domain, FD: fd, devtools: devtools, tmpl: tmpl, config: config}
	s.current.Store(&immutable{status: StatusPassive})
	slog.Debug("created socket", "sock", s)
	return s, nil
}

func (s *Socket) LogValue() slog.Value {
	var status string
	var extra []slog.Attr
	switch cur := s.current.Load(); cur.status {
	case StatusPassive:
		status = "passive"
	case StatusConnected:
		status = "connected"
		extra = append(extra, slog.Any("proxy", cur.connected.proxy))
	case StatusConnecting:
		status = "connecting"
		extra = append(extra, slog.Any("bind", cur.connecting.bind), slog.Any("peer", cur.connecting.peer))
	case StatusListening:
		status = "listening"
		extra = append(extra, slog.String("bind", cur.listening.lis.Addr().String()))
		extra = append(extra, slog.Bool("active", cur.listening.active.Load()))
	case StatusClosed:
		status = "closed"
	}

	var domain string
	switch s.Domain {
	case unix.AF_INET:
		domain = "AF_INET"
	case unix.AF_INET6:
		domain = "AF_INET6"
	}

	return slog.GroupValue(append([]slog.Attr{
		slog.String("domain", domain),
		slog.String("fd", s.FD.String()),
		slog.String("status", status),
	}, extra...)...)
}

func (s *Socket) isBlocking() (bool, error) {
	flags, err := unix.FcntlInt(uintptr(s.FD.FD()), unix.F_GETFL, 0)
	if err != nil {
		return false, fmt.Errorf("get fcntl flags: %w", err)
	}
	return flags&unix.O_NONBLOCK == 0, nil
}

func (s *Socket) Connect(addr netip.AddrPort) (syscall.Errno, error) {
	if !s.FD.IncRef() {
		return unix.EBADF, nil
	}
	defer s.FD.DecRef()

	prev := s.current.Load()
	switch prev.status {
	case StatusPassive:
		break
	case StatusConnected:
		return unix.EISCONN, nil
	case StatusConnecting:
		return unix.EALREADY, nil // TODO: only if the socket is non-blocking
	case StatusListening:
		return unix.EINVAL, nil // TODO: what does linux say if you try to connect a listening socket?
	case StatusClosed:
		return unix.EBADF, nil
	}

	proxy := newProxy(s.devtools, s.tmpl, true, s.config)

	isBlocking, err := s.isBlocking()
	if err != nil {
		return 0, fmt.Errorf("check if socket is blocking: %w", err)
	}

	bind, errno, err := prev.getRemoteBindAddr()
	if err != nil {
		return 0, fmt.Errorf("get bind addr: %w", err)
	}
	if errno != 0 {
		return errno, nil
	}

	slog.Debug("attempting socket connect", "sock", s, "addr", addr, "bind", bind, "isBlocking", isBlocking)

	mid := &immutable{status: StatusConnecting}
	mid.connecting.bind = prev.passive.bind
	mid.connecting.peer = addr

	if mid.connecting.bind == nil {
		var err error
		mid.connecting.bind, err = newTempBindSocket(s.Domain)
		if err != nil {
			return 0, fmt.Errorf("create temp bind socket: %w", err)
		}
		bind, err = bindEphemeral(s.Domain, mid.connecting.bind, false)
		if err != nil {
			if !mid.connecting.bind.ClosingIncRef() {
				panic("failed to incref local temp bind socket?") // there should be no other refs
			}
			defer mid.connecting.bind.DecRef()
			mid.connecting.bind.Lock()
			unix.Close(mid.connecting.bind.FD())
			return 0, fmt.Errorf("bind ephemeral: %w", err)
		}
	}

	if !s.current.CompareAndSwap(prev, mid) {
		if prev.passive.bind == nil && mid.connecting.bind.ClosingIncRef() {
			defer mid.connecting.bind.DecRef()
			mid.connecting.bind.Lock()
			unix.Close(mid.connecting.bind.FD())
		}
		return unix.ERESTART, nil
	}

	dummyCtx, dummyCancel := context.WithCancel(context.Background())
	dummy, err := newDummyListener(dummyCtx, s.Domain)
	if err != nil {
		dummyCancel()
		return 0, fmt.Errorf("create dummy listener: %w", err)
	}

	var wg sync.WaitGroup
	var errDummyAccept, errDialExternal error

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer dummy.lis.Close()

		conn, err := dummy.lis.Accept()
		if err != nil {
			errDummyAccept = fmt.Errorf("accept dummy listener: %w", err)
			return
		}
		proxy.process = conn.(*net.TCPConn)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		d := &net.Dialer{
			Control: func(_, _ string, c syscall.RawConn) error {
				var ret error
				if err := c.Control(func(fd uintptr) {
					if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
						ret = fmt.Errorf("set SO_REUSEADDR=1: %w", err)
						return
					}
					if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
						ret = fmt.Errorf("set SO_REUSEPORT=1: %w", err)
						return
					}
				}); err != nil {
					return fmt.Errorf("control: %w", err)
				}
				return ret
			},
		}
		if bind.IsValid() {
			d.LocalAddr = &net.TCPAddr{IP: bind.Addr().AsSlice(), Port: int(bind.Port())}
		}

		conn, err := d.DialContext(context.TODO(), "tcp", addr.String())
		if err != nil {
			slog.Debug("failed to connect to external", "sock", s, "addr", addr, "err", err, "duration", time.Since(proxy.begin).Nanoseconds()/1000)
			errDialExternal = fmt.Errorf("non-blocking connect: dial external: %w", err)
			return
		}
		slog.Debug("connected to external", "sock", s, "addr", addr, "took", time.Since(proxy.begin).Nanoseconds()/1000)
		proxy.external = conn.(*net.TCPConn)
	}()

	errnoConnect := make(chan syscall.Errno, 1)
	go func() {
		defer dummyCancel()
		wg.Wait()

		var next *immutable
		var errno unix.Errno

		if err := errDummyAccept; err != nil {
			// Check errDummyAccept before errDialExternal because the dummy listener's
			// accept will almost never fail while the external dial may fail in many
			// ways (maybe the remote address is unreachable, maybe the connection was
			// refused, or maybe something else).
			slog.Error("failed to accept on dummy listener", "err", err)
			errno = unix.ENOSYS
			goto out
		}

		if err := errDialExternal; err != nil {
			if !errors.As(err, &errno) {
				slog.Error("failed to interpret non-blocking dial external error as syscall.Errno", "err", err, "type", fmt.Sprintf("%T", err))
				errno = unix.ENOSYS
				goto out
			}

			next = &immutable{status: StatusPassive}
			next.passive.bind = mid.connecting.bind
			next.passive.errno = errno
			goto out
		}

		next = &immutable{status: StatusConnected}
		next.connected.proxy = proxy
		go proxy.start()

	out:

		shouldCloseBind := true
		if next != nil {
			if !s.current.CompareAndSwap(mid, next) {
				errno = unix.ERESTART
			} else {
				// We created a temporary socket earlier (mid.connecting.bind) in case this
				// was a non-blocking connect so that the tracee's getsockname calls will
				// behave correctly in the time after the tracee's connect and before the
				// immutable state CAS.
				//
				// If the connect failed due to application-specific reasons such as
				// ECONNREFUSED, next.status will be StatusPassive and there will
				// remain a reference to bind in next.passive.bind so that a future
				// getsockopt(2) can propagate that error back to the tracee. In this
				// scenario, if the CAS succeeds, we must not close mid.connecting.bind
				// so that next.passive.bind will remain open. In all other cases, it's
				// guaranteed that there won't be any more new references to
				// mid.connecting.bind, so it's okay to close the socket.
				if next.status == StatusPassive {
					shouldCloseBind = false
				}
			}
		}

		// Send errno as early as possible in case this is a blocking connect.
		errnoConnect <- errno

		if errno != 0 {
			if proxy.process != nil {
				proxy.process.Close()
			}
			if proxy.external != nil {
				proxy.external.Close()
			}
		}

		if shouldCloseBind {
			// mid.connecting.bind may have already been closed in the time it took
			// to connect to the external endpoint. Therefore, unlike most other
			// places, it's not an error if this ClosingIncRef fails.
			if mid.connecting.bind.ClosingIncRef() {
				defer mid.connecting.bind.DecRef()
				mid.connecting.bind.Lock()
				unix.Close(mid.connecting.bind.FD())
			}
		}
	}()

	// Normally, with non-blocking connect(2) calls, after the kernel queues the
	// SYN packet and returns EINPROGRESS, the socket would not be writable until
	// the peer's SYN-ACK is received and the ACK reply is queued. Applications
	// can wait for the connection to be established with a poll(2), ppoll(2),
	// epoll(2), or select(2) using POLLOUT on the socket. If the connection
	// fails -- maybe the network is down, the host is unroutable, the connection
	// was refused by the peer, and so on -- the poll would return with POLLERR
	// and/or POLLHUP and the application can use SO_ERROR to find out why.
	// Depending on the network latency between the two hosts, there could be up
	// several hundred milliseconds between the two events.
	//
	// Currently, the behavior of an application under subtrace is different as
	// the process' socket would immediately be writable and the application
	// would perceive the connect to have completed in microseconds no matter how
	// far the peer is. Additionally, if the net.Dial in the above goroutine
	// fails and we close the dummy listener's connection with the target, all
	// connect(2) errors will be perceived as ECONNREFUSED no matter the real
	// reason (TODO: workaround: intercept getsockopt(SO_ERROR) calls).
	//
	// I tried multiple approaches but none of them were satisfactory:
	//
	// (1) Leaving the target's socket unconnected, returning EINPROGRESS,
	//     waiting for the net.Dial to succeed, and then doing dummy connect(2)
	//     caused POLLHUPs and ENOTCONNs in the application's poll while we were
	//     waiting for the net.Dial. This straight up breaks applications.
	//
	// (2) Attaching a BPF filter on the dummy listener using SO_ATTACH_FILTER to
	//     drop all SYN packets until the external net.Dial succeeds and then
	//     detaching the filter emulated the SYN -> SYN-ACK delay correctly, but
	//     it added 1+ second to the connect latency because Linux's TCP
	//     retransmission timer on the process socket waits at least that long
	//     before retransmitting the SYN. Calling connect(2) on a non-blocking
	//     socket that's already connecting returns EALREADY without changing the
	//     retransmission timer state. I tried to workaround this by removing the
	//     BPF filter, then using AF_UNSPEC to dissolve the first connect after
	//     the net.Dial succeeds, and then and then doing a second connect(2).
	//     This worked at first, but upon closer inspection with strace, I
	//     noticed that the target's poll would return POLLERR. This caused a
	//     race after the AF_UNSPEC call between our second connect(2) and the
	//     application's SO_ERROR check which would occassionally result in the
	//     connection being perceived as refused. I tried various TCP setsockopts
	//     (some undocumented) in the hope that one of them would cause the
	//     retransmission timer to short as a side-effect, but none did. Neither
	//     a 1+ second increase in latency for all connects nor occassionally
	//     dropping connections is acceptable. I wish Linux would let us bypass
	//     the TCP retransmission timer from userspace.
	//
	// (3) Making the dummy listener a SOCK_RAW or an AF_PACKET socket would give
	//     us complete control over when the SYN-ACK reply is sent, but creating
	//     such sockets requires CAP_NET_ADMIN and I'd really like to avoid
	//     requiring root as much as possible. This can be worked around by
	//     creating a new network namespace with effective uid=0 using
	//     unshare(2), but this added a huge performance overhead: every packet
	//     would need to go through 6 copies: target memory, kernelspace,
	//     subtrace in the new netns, kernelspace, subtrace in the main netns,
	//     userspace TCP/IP stack, and finally back to kernelspace to go out into
	//     the real world. I also found that the complexity from adding a full
	//     blown userspace TCP/IP stack (gvisor netstack) made subtrace fragile.
	//     It also meant adopting netstack's bugs, including ones that will be
	//     introduced in the future.
	//
	// For most applications and workloads, the current approach is functionally
	// unchanged (ex: the overall HTTP request-response latency and failure modes
	// are unchanged), but this still isn't ideal because we *really* want
	// applications to behave exactly the same way with and without subtrace.
	//
	// TODO(adtac): find a better approach
	var dummyErrno syscall.Errno
	if err := unix.Connect(s.FD.FD(), dummy.sockaddr()); err != nil {
		if !errors.As(err, &dummyErrno) {
			panic(fmt.Errorf("failed to interpret connect(2) error as errno: %w", err))
		}
	}

	if isBlocking {
		if errno := <-errnoConnect; errno != 0 {
			return errno, nil
		}
	}

	if isBlocking {
		slog.Debug("connected blocking socket", "sock", s, "addr", addr, "errno", dummyErrno)
	} else {
		slog.Debug("started non-blocking connect", "sock", s, "addr", addr, "errno", dummyErrno)
	}
	return dummyErrno, nil
}

// Bind binds the socket to the given address. Internally, it uses a dummy
// temporary socket in order to check if the address is bindable and also
// reserve the address for future operations.
func (s *Socket) Bind(addr netip.AddrPort) (syscall.Errno, error) {
	if !s.FD.IncRef() {
		return unix.EBADF, nil
	}
	defer s.FD.DecRef()

	prev := s.current.Load()
	switch prev.status {
	case StatusPassive:
		break
	case StatusConnected, StatusConnecting, StatusListening:
		return unix.EINVAL, nil
	case StatusClosed:
		return unix.EBADF, nil
	}

	if s.Domain == unix.AF_INET && !addr.Addr().Is4() {
		return unix.EINVAL, nil
	}
	if s.Domain == unix.AF_INET6 && !addr.Addr().Is6() {
		return unix.EINVAL, nil
	}

	next := &immutable{status: StatusPassive}
	next.passive.bind = prev.passive.bind
	if next.passive.bind == nil {
		var err error
		next.passive.bind, err = newTempBindSocket(s.Domain)
		if err != nil {
			return 0, fmt.Errorf("create temp bind socket: %w", err)
		}
	}

	if !next.passive.bind.IncRef() {
		return unix.EBADF, nil
	}
	defer next.passive.bind.DecRef()

	var sa unix.Sockaddr
	switch s.Domain {
	case unix.AF_INET:
		sa = &unix.SockaddrInet4{Addr: addr.Addr().As4(), Port: int(addr.Port())}
	case unix.AF_INET6:
		sa = &unix.SockaddrInet6{Addr: addr.Addr().As16(), Port: int(addr.Port())}
	}

	if err := unix.Bind(next.passive.bind.FD(), sa); err != nil {
		if prev.passive.bind == nil {
			unix.Close(next.passive.bind.FD())
		}
		next := &immutable{status: StatusPassive}
		next.passive.bind = prev.passive.bind
		if !errors.As(err, &next.passive.errno) {
			return 0, fmt.Errorf("bind: %w", err)
		}
		if !s.current.CompareAndSwap(prev, next) {
			return unix.ERESTART, nil
		}
		return next.passive.errno, nil
	}

	if !s.current.CompareAndSwap(prev, next) { // TODO: unbind?
		if prev.passive.bind == nil {
			unix.Close(next.passive.bind.FD())
		}
		return unix.ERESTART, nil
	}

	slog.Debug("bound socket to address", "sock", s, "addr", addr)
	return 0, nil
}

func (s *Socket) BindAddr() (netip.AddrPort, syscall.Errno, error) {
	if !s.FD.IncRef() {
		return netip.AddrPort{}, unix.EBADF, nil
	}
	defer s.FD.DecRef()
	return s.current.Load().getRemoteBindAddr()
}

func (s *Socket) PeerAddr() (netip.AddrPort, syscall.Errno, error) {
	if !s.FD.IncRef() {
		return netip.AddrPort{}, unix.EBADF, nil
	}
	defer s.FD.DecRef()
	return s.current.Load().getRemotePeerAddr()
}

func (s *Socket) Errno() unix.Errno {
	cur := s.current.Load()
	switch cur.status {
	case StatusPassive:
		return cur.passive.errno
	default:
		return 0
	}
}

func (s *Socket) Listen(backlog int) (syscall.Errno, error) {
	if !s.FD.IncRef() {
		return unix.EBADF, nil
	}
	defer s.FD.DecRef()

	prev := s.current.Load()
	switch prev.status {
	case StatusPassive:
		break
	case StatusConnected, StatusConnecting:
		return unix.EINVAL, nil // TODO: what does linux say if you try to listen a connected socket?
	case StatusListening:
		return 0, nil
	case StatusClosed:
		return unix.EBADF, nil
	}

	// TODO(adtac): I think the Linux kernel also enforces a minimum like this,
	// but maybe it's configurable?
	if backlog < 8 {
		backlog = 8
	}

	ephemeral, err := bindEphemeral(s.Domain, s.FD, true)
	if err != nil {
		return 0, fmt.Errorf("bind ephemeral: %w", err)
	}

	bind, errno, err := prev.getRemoteBindAddr()
	if err != nil {
		return 0, fmt.Errorf("get bind addr: %w", err)
	}
	if errno != 0 {
		return errno, nil
	}

	var lis net.Listener

	switch s.Domain {
	case unix.AF_INET:
		if !bind.IsValid() {
			lis, err = net.Listen("tcp4", "127.0.0.1:0")
		} else {
			lis, err = net.Listen("tcp4", bind.String())
		}
	case unix.AF_INET6:
		if !bind.IsValid() {
			lis, err = net.Listen("tcp6", "[::1]:0")
		} else if bind.Addr().IsUnspecified() {
			// [::]:80 seems to listen on both IPv4 and IPv6 but 127.0.0.1:80 doesn't?
			lis, err = net.Listen("tcp", bind.String())
		} else {
			lis, err = net.Listen("tcp6", bind.String())
		}
	}
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return errno, nil
		}
		return 0, fmt.Errorf("external side listen: %w", err)
	}

	if err := unix.Listen(s.FD.FD(), backlog); err != nil {
		lis.Close()
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return errno, nil
		}
		return 0, fmt.Errorf("virtual listen: %w", err)
	}

	next := &immutable{status: StatusListening}
	next.listening.active.Store(true)
	next.listening.lis = lis
	if !s.current.CompareAndSwap(prev, next) {
		lis.Close()
		return unix.ERESTART, nil
	}

	// Separate goroutines for the accept loop and the dispatch loop so that
	// buffer channel can act as both a fixed size buffer and a rate limiter.
	buffer := make(chan *proxy, backlog*2)

	go func() { // accept loop
		defer lis.Close()
		defer next.listening.active.Store(false)
		defer close(buffer)
		for {
			external, err := lis.Accept()
			switch {
			case err == nil:
				p := newProxy(s.devtools, s.tmpl, false, s.config)
				p.external = external.(*net.TCPConn)
				buffer <- p
			case errors.Is(err, net.ErrClosed):
				return
			default:
				slog.Error("failed to accept incoming connection", "sock", s, "err", err)
				return
			}
		}
	}()

	go func() { // dispatch loop
		for conn := range buffer {
			go func(p *proxy) {
				process, err := net.Dial("tcp", ephemeral.String())
				if err != nil {
					p.external.Close()
					slog.Debug("failed to dial ephemeral address", "err", err) // not fatal: the process probably exited
					return
				}
				p.process = process.(*net.TCPConn)

				addr := netip.MustParseAddrPort(process.LocalAddr().String())
				if addr.Addr().Is4In6() {
					addr = netip.AddrPortFrom(netip.AddrFrom4(addr.Addr().As4()), addr.Port())
				}

				ch := make(chan *proxy, 1)
				if found, loaded := next.listening.backlog.LoadOrStore(addr, ch); loaded {
					ch = found.(chan *proxy)
					next.listening.backlog.Delete(addr)
				}
				ch <- p
				slog.Debug("dispatcher enqueued accepted connection", "sock", s, "addr", addr)
			}(conn)
		}
	}()

	slog.Debug("marked socket as listening", "sock", s, "addr", bind)
	return 0, nil
}

func (s *Socket) Accept(flags int) (*Socket, syscall.Errno, error) {
	if !s.FD.IncRef() {
		return nil, unix.EBADF, nil
	}
	defer s.FD.DecRef()

	cur := s.current.Load()
	switch cur.status {
	case StatusPassive, StatusConnected, StatusConnecting:
		return nil, unix.EINVAL, nil
	case StatusListening:
		if !cur.listening.active.Load() {
			return nil, unix.EINVAL, nil // TODO: right errno?
		}
	case StatusClosed:
		return nil, unix.EBADF, nil
	}

	ret, sa, err := unix.Accept4(s.FD.FD(), flags|unix.SOCK_CLOEXEC)
	if err != nil {
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			return nil, 0, fmt.Errorf("failed to interpret accept error as errno: %w", err)
		}
		// If accept(2) fails, Linux does not put the socket in an error state.
		return nil, errno, nil
	}

	var addr netip.AddrPort
	switch sa := sa.(type) {
	case *unix.SockaddrInet4:
		addr = netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port))
	case *unix.SockaddrInet6:
		addr = netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), uint16(sa.Port))
	}
	if addr.Addr().Is4In6() {
		addr = netip.AddrPortFrom(netip.AddrFrom4(addr.Addr().As4()), addr.Port())
	}

	ch := make(chan *proxy, 1)
	if found, loaded := cur.listening.backlog.LoadOrStore(addr, ch); loaded {
		ch = found.(chan *proxy)
		cur.listening.backlog.Delete(addr)
	}

	p := <-ch
	if p.process.LocalAddr().String() != addr.String() {
		panic(fmt.Sprintf("dialed process-side local does not match accepted connection: %s != %s", p.process.LocalAddr(), addr))
	}
	slog.Debug("accepter dequeued accepted connection", "sock", s, "addr", addr)

	child := &Socket{Domain: s.Domain, devtools: s.devtools, tmpl: s.tmpl, config: s.config}
	child.FD = fd.NewFD(ret)
	defer child.FD.DecRef()

	state := &immutable{status: StatusConnected}
	state.connected.proxy = p
	child.current.Store(state)
	go p.start()

	return child, 0, nil
}

func (s *Socket) Close() syscall.Errno {
	if !s.FD.ClosingIncRef() {
		return unix.EBADF
	}
	defer s.FD.DecRef()

	s.FD.Lock()
	if err := unix.Close(s.FD.FD()); err != nil {
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			panic(fmt.Errorf("cannot interpret close(2) error as errno: %w", err))
		}
		return errno
	}

	var errs []error
	var prev *immutable
	for {
		prev = s.current.Load()
		if prev.status == StatusClosed {
			panic("entered closing incref because file descriptor was not close, but current status is closed")
		}
		next := &immutable{status: StatusClosed}
		if s.current.CompareAndSwap(prev, next) {
			break
		}
	}

	switch prev.status {
	case StatusPassive:
		if prev.passive.bind != nil && prev.passive.bind.ClosingIncRef() {
			defer prev.passive.bind.DecRef()
			prev.passive.bind.Lock()
			if err := unix.Close(prev.passive.bind.FD()); err != nil {
				errs = append(errs, fmt.Errorf("close temp bind socket: %w", err))
			}
		}

	case StatusConnected:
		if prev.connected.proxy.skipCloseTCP.CompareAndSwap(false, true) {
			// We can't close the two underlying TCP connections yet because the
			// proxy might still have some unflushed bytes in a buffer somewhere. The
			// connections will be closed when the (*proxy).start() goroutine ends.
			// See the equivalent CAS in proxy.go for the process.Close() and
			// external.Close() calls.
		} else {
			if err := prev.connected.proxy.process.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("close process conn: %w", err))
			}
			if err := prev.connected.proxy.external.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("close external conn: %w", err))
			}
		}

	case StatusConnecting:
		if prev.connecting.bind.ClosingIncRef() {
			defer prev.connecting.bind.DecRef()
			prev.connecting.bind.Lock()
			if err := unix.Close(prev.connecting.bind.FD()); err != nil {
				errs = append(errs, fmt.Errorf("close temp bind socket: %w", err))
			}
		}

	case StatusListening:
		// If the listening goroutine has already exited (maybe something went
		// wrong with the listener), don't try to close the listener again.
		if prev.listening.active.CompareAndSwap(true, false) {
			if err := prev.listening.lis.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close listener: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		slog.Debug("closing socket encountered non-fatal errors", "sock", s, "errs", errs)
	} else {
		slog.Debug("closed socket", "sock", s)
	}
	return 0
}

type dummyListener struct {
	lis  net.Listener
	addr netip.AddrPort
}

func newDummyListener(ctx context.Context, domain int) (*dummyListener, error) {
	var addr netip.AddrPort
	var network string
	switch domain {
	case unix.AF_INET:
		network = "tcp4"
		addr = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 0)
	case unix.AF_INET6:
		network = "tcp6"
		addr = netip.AddrPortFrom(netip.AddrFrom16([16]byte{15: 1}), 0)
	}

	lis, err := new(net.ListenConfig).Listen(ctx, network, addr.String())
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	addr, err = netip.ParseAddrPort(lis.Addr().String())
	if err != nil {
		lis.Close()
		return nil, fmt.Errorf("parse addr: %w", err)
	}
	return &dummyListener{lis: lis, addr: addr}, nil
}

func (d *dummyListener) sockaddr() unix.Sockaddr {
	switch {
	case d.addr.Addr().Is4():
		return &unix.SockaddrInet4{Addr: d.addr.Addr().As4(), Port: int(d.addr.Port())}
	case d.addr.Addr().Is6():
		return &unix.SockaddrInet6{Addr: d.addr.Addr().As16(), Port: int(d.addr.Port())}
	default:
		panic(fmt.Sprintf("invalid AddrPort %s", d.addr.String()))
	}
}

// newTempBindSocket creates a temporary socket to use as a parking spot for an
// address bind. The returned socket has SO_REUSEADDR and SO_REUSEPORT set to 1.
func newTempBindSocket(domain int) (*fd.FD, error) {
	ret, err := unix.Socket(domain, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		return nil, fmt.Errorf("create temp bind socket: %w", err)
	}
	fd := fd.NewFD(ret)
	defer fd.DecRef()

	if err := unix.SetsockoptInt(fd.FD(), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		unix.Close(fd.FD())
		return nil, fmt.Errorf("set SO_REUSEADDR: %w", err)
	}
	if err := unix.SetsockoptInt(fd.FD(), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		unix.Close(fd.FD())
		return nil, fmt.Errorf("set SO_REUSEPORT: %w", err)
	}
	return fd, nil
}

func getEphemeralLoopbackAddr(domain int) ([]byte, error) {
	arr, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var errs []error
	for _, iface := range arr {
		addrs, err := iface.Addrs()
		if err != nil {
			errs = append(errs, fmt.Errorf("list addresses %s: %w", iface.Name, err))
			continue
		}

		for _, addr := range addrs {
			// slog.Debug("found address on network interface", "iface", iface.Name, slog.Group("addr", "type", fmt.Sprintf("%T", addr), "val", addr))
			switch addr := addr.(type) {
			case *net.IPNet:
				if !addr.IP.IsLoopback() {
					continue
				}
				if domain == unix.AF_INET && addr.IP.To4() != nil {
					return addr.IP.To4(), nil
				}
				if domain == unix.AF_INET6 && addr.IP.To4() == nil {
					return addr.IP.To16(), nil
				}
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("no loopback address found")
}

// bindEphemeral binds a socket to an ephemeral address.
func bindEphemeral(domain int, fd *fd.FD, loopback bool) (netip.AddrPort, error) {
	if !fd.IncRef() {
		return netip.AddrPort{}, unix.EBADF
	}
	defer fd.DecRef()

	var addr []byte
	var sa unix.Sockaddr
	switch domain {
	case unix.AF_INET:
		sa = &unix.SockaddrInet4{}
		if loopback {
			if val, err := getEphemeralLoopbackAddr(domain); err == nil {
				copy(sa.(*unix.SockaddrInet4).Addr[:], val)
				addr = sa.(*unix.SockaddrInet4).Addr[:]
			}
		}
	case unix.AF_INET6:
		sa = &unix.SockaddrInet6{}
		if loopback {
			if val, err := getEphemeralLoopbackAddr(domain); err == nil {
				copy(sa.(*unix.SockaddrInet6).Addr[:], val)
				addr = sa.(*unix.SockaddrInet6).Addr[:]
			}
		}
	default:
		panic(fmt.Sprintf("unknown domain %d", domain))
	}

	if loopback || len(addr) > 0 {
		slog.Debug("binding ephemeral socket", "domain", domain, "fd", fd.String(), "loopback", loopback, slog.Group("sockaddr", "type", fmt.Sprintf("%T", sa), "addr", net.IP(addr)))
	}
	if err := unix.Bind(fd.FD(), sa); err != nil {
		return netip.AddrPort{}, fmt.Errorf("bind %T: addr %v: %w", sa, net.IP(addr), err)
	}

	sa, err := unix.Getsockname(fd.FD())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("get ephemeral address: %w", err)
	}

	switch addr := sa.(type) {
	case *unix.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(addr.Addr), uint16(addr.Port)), nil
	case *unix.SockaddrInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(addr.Addr), uint16(addr.Port)), nil
	default:
		panic(fmt.Sprintf("unknown sockaddr type %T", sa))
	}
}
