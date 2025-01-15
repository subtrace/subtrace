package tracer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
	"subtrace.dev/rpc"
	"subtrace.dev/tunnel"
)

var DefaultManager = newManager()

type block struct {
	mu      sync.Mutex
	events  []string
	frozen  bool
	flushed bool
	count   atomic.Uint64 // approx number of events
	bytes   atomic.Uint64 // approx total size
}

func (b *block) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = nil
	b.frozen = false
	b.flushed = false
	b.count.Store(0)
	b.bytes.Store(0)
}

func (b *block) insert(event string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return false
	}
	b.events = append(b.events, event)
	b.count.Add(1)
	b.bytes.Add(uint64(len(event)))
	return true
}

func initTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string) (_ *websocket.Conn, finalErr error) {
	slog.Debug("initializing tunnel session", "tunnelID", tunnelID, "role", tunnel.Role_INSERT)

	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: rpc.GetHeader(
			rpc.WithoutToken(),
			rpc.WithTag("subtrace_tunnel_id", tunnelID.String()),
			rpc.WithTag("subtrace_tunnel_side", "source"),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		if conn != nil {
			conn.CloseNow()
		}
		err := fmt.Errorf("bad response status: got %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
		if resp.Body != nil {
			if b, _ := io.ReadAll(resp.Body); len(b) > 0 {
				err = fmt.Errorf("%w: %s", err, string(b))
			}
		}
		return nil, err
	}
	return conn, nil
}

func doInsert(ctx context.Context, conn *websocket.Conn, events []string) (int, error) {
	// TODO(adtac): it's wasteful to re-encode and copy the data into yet another
	// byte buffer (similarly for the read). Consider using protodelim instead?
	tunnelQueryID := uuid.New()
	qmsg, err := proto.Marshal(&tunnel.Insert{TunnelQueryId: tunnelQueryID.String(), Events: events})
	if err != nil {
		return 0, fmt.Errorf("query: marshal: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, qmsg); err != nil {
		return len(qmsg), fmt.Errorf("query: write: %w", err)
	}

	var result tunnel.Result
	typ, rmsg, err := conn.Read(ctx)
	if err != nil {
		return len(qmsg), fmt.Errorf("result: read: %w", err)
	}
	if typ != websocket.MessageBinary {
		return len(qmsg), fmt.Errorf("result: unexpected websocket message type %d", typ)
	}
	if err := proto.Unmarshal(rmsg, &result); err != nil {
		return len(qmsg), fmt.Errorf("result: unmarshal: %w", err)
	}

	if result.TunnelQueryId != tunnelQueryID.String() {
		// TODO(adtac): This shouldn't really be an error. We're opening a new
		// tunnel session for every INSERT, which is wasteful. When we start
		// reusing the same tunnel for multiple queries, there needs to be a tunnel
		// manager that routes results to the right query based on the query ID.
		return len(qmsg), fmt.Errorf("got result query ID %s, want %s", result.TunnelQueryId, tunnelQueryID.String())
	}
	if result.TunnelError != "" {
		return len(qmsg), fmt.Errorf("tunnel error: %s", result.TunnelError)
	}
	if result.ClickhouseError != "" {
		return len(qmsg), fmt.Errorf("clickhouse error: %s", result.ClickhouseError)
	}
	return len(qmsg), nil
}

func (b *block) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Attr{Key: "ptr", Value: slog.StringValue(fmt.Sprintf("%p", b))},
		slog.Attr{Key: "count", Value: slog.Uint64Value(b.count.Load())},
		slog.Attr{Key: "bytes", Value: slog.Uint64Value(b.bytes.Load())},
	)
}

func (b *block) flushOnce(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return fmt.Errorf("block already flushed")
	}

	b.frozen = true

	// Check the actual array instead of count to see if the block is empty.
	// b.count exists only because it's nice to log the approximate size of the
	// data in the block without locking it.
	if len(b.events) == 0 {
		b.flushed = true
		return nil
	}

	slog.Debug("flushing event buffer block", "block", b)

	var tun tunnel.Create_Response
	if code, err := rpc.Call(ctx, &tun, "/api/CreateTunnel", &tunnel.Create_Request{Role: tunnel.Role_INSERT}); err != nil {
		return fmt.Errorf("call CreateTunnel: %w", err)
	} else if code != http.StatusOK || tun.Error != "" {
		err := fmt.Errorf("CreateTunnel: %s", http.StatusText(code))
		if tun.Error != "" {
			err = fmt.Errorf("%w: %s", err, tun.Error)
		}
		return err
	}

	tunnelID, err := uuid.Parse(tun.TunnelId)
	if err != nil {
		return fmt.Errorf("parse tunnelID: %w", err)
	}
	slog.Debug("created tunnel to push event buffer block", "block", b, "tunnelID", tunnelID)

	start := time.Now()
	conn, err := initTunnel(ctx, tunnelID, tun.Endpoint)
	if err != nil {
		var wsErr websocket.CloseError
		if errors.As(err, &wsErr) && wsErr.Code == websocket.StatusGoingAway {
			return fmt.Errorf("init tunnel: tunnel %s: timed out waiting for sink after %v", tunnelID.String(), time.Since(start))
		}
		return fmt.Errorf("init tunnel: tunnel %s: %w", tunnelID, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if _, err := doInsert(ctx, conn, b.events); err != nil {
		return fmt.Errorf("insert %d events (%d bytes): %w", b.count.Load(), b.bytes.Load(), err)
	}

	b.flushed = true
	slog.Debug("flushed data to clickhouse", "block", b, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func (b *block) flush(ctx context.Context) error {
	if os.Getenv("SUBTRACE_TOKEN") == "" {
		return nil
	}

	err := b.flushOnce(ctx)
	if err == nil {
		return nil
	}

	rand := rand.New(rand.NewSource(time.Now().UnixNano()))
	wait := 5000 + time.Duration(rand.Intn(5000))*time.Millisecond
	slog.Debug("failed to flush block, retrying after backoff wait", "block", b, "err", err, "wait", wait)
	time.Sleep(wait)

	if err := b.flushOnce(ctx); err != nil {
		slog.Debug("failed to flush block after retry, dropping all events in block", "block", b, "err", err)
		return err
	}
	return nil
}

type Manager struct {
	pool sync.Pool
	cur  atomic.Pointer[block]
	log  atomic.Bool
}

func newManager() *Manager {
	m := &Manager{pool: sync.Pool{New: func() any { return new(block) }}}
	m.cur.Store(m.pool.Get().(*block))
	return m
}

func (m *Manager) put(b *block) {
	b.reset()
	m.pool.Put(b)
}

func (m *Manager) finalize(b *block) error {
	defer m.put(b)
	if err := b.flush(context.TODO()); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

func (m *Manager) Insert(event string) {
	if m.log.Load() {
		fmt.Fprintf(os.Stderr, "%s\n", event)
	}

	var next *block
	for {
		cur := m.cur.Load()
		if cur.insert(event) {
			if next != nil {
				m.put(next)
			}
			return
		}

		if next == nil {
			next = m.pool.Get().(*block)
		}

		if m.cur.CompareAndSwap(cur, next) {
			go m.finalize(cur)
			next = nil
		}
	}
}

func (m *Manager) Flush() error {
	next := m.pool.Get().(*block)
	for {
		cur := m.cur.Load()
		if m.cur.CompareAndSwap(cur, next) {
			return m.finalize(cur)
		}
	}
}

func (m *Manager) SetLog(log bool) {
	m.log.Store(log)
}

func (m *Manager) StartBackgroundFlush(ctx context.Context) {
	period := 5 * time.Second
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if backoff >= period {
			slog.Debug("skipping background flush tick due to remaining backoff timer", "backoff", backoff)
			backoff -= period
			continue
		} else {
			backoff = 0
		}

		if err := m.Flush(); err != nil {
			slog.Error("failed to flush block", "err", err)
			if backoff < period {
				backoff += time.Second
			} else {
				backoff *= 2
			}
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
	}
}
