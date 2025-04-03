package socket

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/fd"
)

const (
	StatePassive = iota
	StateConnected
	StateConnecting
	StateListening
	StateClosed
)

type ImmutableState struct {
	state int

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

func (s *ImmutableState) getRemoteBindAddr() (netip.AddrPort, syscall.Errno, error) {
	switch s.state {
	case StatePassive:
		if s.passive.bind == nil {
			return netip.AddrPort{}, 0, nil
		}
		return getsockname(s.passive.bind)

	case StateConnected:
		addr, err := netip.ParseAddrPort(s.connected.proxy.external.LocalAddr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("connected: parse addr: %w", err)
		}
		return addr, 0, nil

	case StateConnecting:
		if s.connecting.bind == nil {
			panic("connecting socket has no bind")
		}
		return getsockname(s.connecting.bind)

	case StateListening:
		addr, err := netip.ParseAddrPort(s.listening.lis.Addr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("listen: parse addr: %w", err)
		}
		return addr, 0, nil

	case StateClosed:
		return netip.AddrPort{}, unix.EBADF, nil
	}
	panic("unreachable")
}

func (s *ImmutableState) getRemotePeerAddr() (netip.AddrPort, syscall.Errno, error) {
	switch s.state {
	case StatePassive:
		return netip.AddrPort{}, 0, nil

	case StateConnected:
		addr, err := netip.ParseAddrPort(s.connected.proxy.external.RemoteAddr().String())
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("external conn: parse remote addr: %w", err)
		}
		return addr, 0, nil

	case StateConnecting:
		return s.connecting.peer, 0, nil

	case StateListening:
		return netip.AddrPort{}, unix.EINVAL, nil

	case StateClosed:
		return netip.AddrPort{}, unix.EBADF, nil
	}
	panic("unreachable")
}

type Inode struct {
	Domain   int
	Protocol int
	Number   uint64

	state *atomic.Pointer[ImmutableState]

	mu   sync.RWMutex // TODO: replace with a lock-free linked list if bad perf
	open []*Socket
}

func newInode(domain int, protocol int, number uint64, state *ImmutableState) *Inode {
	if state == nil {
		panic(fmt.Errorf("new inode: %d: missing state", number))
	}

	ino := &Inode{Domain: domain, Protocol: protocol, Number: number, state: new(atomic.Pointer[ImmutableState])}
	ino.state.Store(state)
	return ino
}

func (ino *Inode) LogValue() slog.Value {
	var state string
	var extra []slog.Attr
	switch s := ino.state.Load(); s.state {
	case StatePassive:
		state = "passive"
	case StateConnected:
		state = "connected"
		extra = append(extra, slog.Any("proxy", s.connected.proxy))
	case StateConnecting:
		state = "connecting"
		extra = append(extra, slog.Any("bind", s.connecting.bind), slog.Any("peer", s.connecting.peer))
	case StateListening:
		state = "listening"
		extra = append(extra, slog.String("bind", s.listening.lis.Addr().String()))
		extra = append(extra, slog.Bool("active", s.listening.active.Load()))
	case StateClosed:
		state = "closed"
	}

	var domain string
	switch ino.Domain {
	case unix.AF_INET:
		domain = "AF_INET"
	case unix.AF_INET6:
		domain = "AF_INET6"
	}

	var protocol string
	switch ino.Protocol {
	case unix.IPPROTO_TCP:
		protocol = "IPPROTO_TCP"
	case unix.IPPROTO_MPTCP:
		protocol = "IPPROTO_MPTCP"
	}

	ino.mu.RLock()
	open := len(ino.open)
	ino.mu.RUnlock()

	return slog.GroupValue(append([]slog.Attr{
		slog.String("domain", domain),
		slog.String("protocol", protocol),
		slog.Uint64("number", ino.Number),
		slog.Int("open", open),
		slog.String("state", state),
	}, extra...)...)
}

func (ino *Inode) add(sock *Socket) {
	ino.mu.Lock()
	defer ino.mu.Unlock()

	ino.open = append(ino.open, sock)
}

func (ino *Inode) remove(sock *Socket) (last bool) {
	ino.mu.Lock()
	defer ino.mu.Unlock()

	size := len(ino.open)

	if size == 1 {
		if ino.open[0] != sock {
			panic(fmt.Errorf("untrack: sock=%p: [0]=%p", sock, ino.open[0]))
		}

		ino.open = nil
		return true
	}

	for i := range ino.open {
		if ino.open[i] == sock {
			if i < size-1 {
				ino.open[i], ino.open[size-1] = ino.open[size-1], ino.open[i]
			}

			ino.open = ino.open[:size-1]
			return false
		}
	}

	panic(fmt.Errorf("untrack: sock=%p: cannot find in size=%d list", sock, size))
}

type InodeTable struct {
	mu    sync.RWMutex
	known map[uint64]*Inode
}

func NewInodeTable() *InodeTable {
	return &InodeTable{
		known: make(map[uint64]*Inode),
	}
}

func (t *InodeTable) Get(number uint64) (*Inode, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ino, ok := t.known[number]
	return ino, ok
}

func (t *InodeTable) Add(ino *Inode) {
	if _, ok := t.Get(ino.Number); ok { // fast path
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if old, ok := t.known[ino.Number]; ok {
		if old != ino {
			panic(fmt.Errorf("duplicate inode %d (old=%p, new=%p): old=%q, new=%q", ino.Number, old, ino, old.LogValue().String(), ino.LogValue().String()))
		}
	} else {
		t.known[ino.Number] = ino
	}
}
