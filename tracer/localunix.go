package tracer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

func listenAbstractStable() (*net.UnixListener, int, error) {
	h := sha256.New()

	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return nil, 0, fmt.Errorf("read machine ID: %w", err)
	}
	h.Write([]byte(machineID))
	h.Write([]byte{0})

	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, 0, fmt.Errorf("read boot ID: %w", err)
	}
	h.Write([]byte(bootID))
	h.Write([]byte{0})

	// We want to find a stable, unique reference to the user's terminal window
	// so that invoking `subtrace run` twice will listen on the same Unix
	// address. The TTY tty is a nice stable way to do this. In the future, when
	// we add support for non-Linux systems, it's probably better to start with
	// /dev/stdin to be portable.
	tty, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		return nil, 0, fmt.Errorf("readlink /proc/self/fd/0: %w", err)
	}
	h.Write([]byte(tty))
	h.Write([]byte{0})

	hash := h.Sum(nil)
	rand := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(hash[:8]))))
	tracerID := 1_000 + rand.Intn(9_000)

	lis, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: fmt.Sprintf("@%d.subtrace", tracerID)})
	if err != nil {
		return nil, 0, fmt.Errorf("listen: %w", err)
	}
	return lis, tracerID, nil
}

type abstractListener struct {
	TracerID int

	ctx context.Context
	lis *net.UnixListener

	mu    sync.RWMutex
	conns []*net.UnixConn
	locks []*sync.Mutex
}

var DefaultAbstractListener *abstractListener

func InitAbstractListener(ctx context.Context) error {
	var err error
	DefaultAbstractListener, err = newAbstractListener(ctx)
	return err
}

func newAbstractListener(ctx context.Context) (*abstractListener, error) {
	lis, tracerID, err := listenAbstractStable()
	if err != nil {
		for i := 0; i < 10; i++ {
			rand := rand.New(rand.NewSource(time.Now().UnixNano()))
			id := 100_000 + rand.Intn(900_000)
			lis, err = net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: fmt.Sprintf("@%d.subtrace", id)})
			if err == nil {
				tracerID = id
				break
			}
		}
	}
	if lis == nil {
		return nil, fmt.Errorf("unix subtrace socket space is full")
	}

	s := &abstractListener{
		TracerID: tracerID,
		ctx:      ctx,
		lis:      lis,
	}
	go s.start()
	return s, nil
}

func (a *abstractListener) start() {
	defer a.lis.Close()
	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}

		conn, err := a.lis.AcceptUnix()
		if err != nil {
			select {
			case <-a.ctx.Done():
			default:
				switch {
				case strings.Contains(err.Error(), "use of closed network connection"):
				default:
					slog.Error("failed to accept unix connection from unix listener", "err", err)
				}
			}
			return
		}

		slog.Debug("accepted new unix listener conn", "conn", fmt.Sprintf("%p", conn))
		a.addConn(conn)
	}
}

func (a *abstractListener) Stop() {
	a.lis.Close()
}

func (a *abstractListener) addConn(conn *net.UnixConn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conns = append(a.conns, conn)
	a.locks = append(a.locks, new(sync.Mutex))
}

func (a *abstractListener) removeConn(conn *net.UnixConn) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 0; i < len(a.conns); i++ {
		if a.conns[i] == conn {
			a.conns = append(a.conns[:i], a.conns[i+1:]...)
			a.locks = append(a.locks[:i], a.locks[i+1:]...)
			slog.Debug("removed unix listener conn", "conn", fmt.Sprintf("%p", conn))
			return
		}
	}
}

func (a *abstractListener) send(val []byte) {
	a.mu.RLock()
	conns := append([]*net.UnixConn{}, a.conns...)
	locks := append([]*sync.Mutex{}, a.locks...)
	a.mu.RUnlock()

	for i := range conns {
		go func(i int) {
			locks[i].Lock()
			defer locks[i].Unlock()
			if _, err := conns[i].Write(val); err != nil {
				a.removeConn(conns[i])
			}
		}(i)
	}
}
