package tracer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	mu     sync.Mutex
	events []string
	frozen bool
}

func (b *block) insert(event string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return false
	}
	b.events = append(b.events, event)
	return true
}

func initTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string) (_ *websocket.Conn, finalErr error) {
	slog.Debug("initializing tunnel session", "tunnelID", tunnelID, "role", tunnel.Role_INSERT)

	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"x-subtrace-tags": {fmt.Sprintf("subtrace_tunnel_id=%q subtrace_tunnel_side=%q", tunnelID, "source")},
		},
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

func (b *block) flush(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.frozen {
		return fmt.Errorf("block already frozen")
	}
	b.frozen = true
	defer func() { b.frozen = false }()

	if len(b.events) == 0 {
		return nil
	}

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

	start := time.Now()
	conn, err := initTunnel(ctx, tunnelID, tun.Endpoint)
	if err != nil {
		var wsErr websocket.CloseError
		if errors.As(err, &wsErr) && wsErr.Code == websocket.StatusGoingAway {
			// TODO: should we retry?
			return fmt.Errorf("init tunnel: tunnel %s: timed out waiting for sink after %v", tunnelID.String(), time.Since(start))
		}
		return fmt.Errorf("init tunnel: tunnel %s: %w", tunnelID, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	size, err := doInsert(ctx, conn, b.events)
	if err != nil {
		return fmt.Errorf("do insert (events=%d, size=%d): %w", len(b.events), size, err)
	}

	slog.Debug("flushed data to clickhouse", "events", len(b.events), "size", size, "took", time.Since(start).Round(time.Millisecond))
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

func (m *Manager) Insert(event string) {
	if m.log.Load() {
		fmt.Fprintf(os.Stderr, "%s\n", event)
	}

	var next *block
	for {
		cur := m.cur.Load()
		if cur.insert(event) {
			if next != nil {
				m.pool.Put(next)
			}
			return
		}

		if next == nil {
			next = m.pool.Get().(*block)
		}

		if m.cur.CompareAndSwap(cur, next) {
			next = nil
			go func() {
				err := cur.flush(context.TODO())
				m.pool.Put(cur)
				if err != nil {
					slog.Error("failed to flush block", "err", err)
				}
			}()
		}
	}
}

func (m *Manager) Flush() error {
	next := m.pool.Get().(*block)
	for {
		cur := m.cur.Load()
		if m.cur.CompareAndSwap(cur, next) {
			err := cur.flush(context.Background())
			m.pool.Put(cur)
			return err
		}
	}
}

func (m *Manager) SetLog(log bool) {
	m.log.Store(log)
}

func (m *Manager) StartBackgroundFlush(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	backoff := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if backoff > 0 {
			backoff--
			continue
		}

		if err := m.Flush(); err != nil {
			slog.Error("failed to flush block", "err", err)
			if backoff == 0 {
				backoff = 1
			} else {
				backoff *= 2
			}
			if backoff > 30 {
				backoff = 30
			}
		} else {
			backoff = 0
		}
	}
}
