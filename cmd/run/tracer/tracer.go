package tracer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
	"subtrace.dev/event"
	"subtrace.dev/rpc"
	"subtrace.dev/tunnel"
)

type block struct {
	buf   [1 << 16]byte
	off   atomic.Uint32
	count atomic.Uint32
	wg    sync.WaitGroup
	open  atomic.Bool
}

func (b *block) reset() {
	b.wg.Wait()
	b.off.Store(0)
	b.count.Store(0)
	b.open.Store(true)
}

func (b *block) get(size int) []byte {
	b.wg.Add(1)
	if !b.open.Load() {
		b.wg.Done()
		return nil
	}

	end := int(b.off.Add(uint32(size)))
	if end > cap(b.buf) {
		b.off.Add(^uint32(size - 1)) // off -= size
		b.wg.Done()
		return nil
	}
	return b.buf[end-size : end]
}

// insert serializes and inserts the given event into the block buffer. It
// returns false if there isn't enough space or if the block is closed.
func (b *block) insert(ev *event.Event) bool {
	if !b.open.Load() {
		return false
	}

	opts := proto.MarshalOptions{UseCachedSize: true}
	size := opts.Size(ev)

	pre := 1
	for x := size; x >= 0x80; x >>= 7 {
		pre++
	}

	slice := b.get(pre + size)
	if slice == nil {
		return false
	}
	defer b.wg.Done()

	if got := binary.PutUvarint(slice[:pre], uint64(size)); got != pre {
		panic(fmt.Errorf("marshal event size: got %d byte varint prefix encoding for a proto size of %d, want %d", got, size, pre))
	}
	if _, err := opts.MarshalAppend(slice[:pre], ev); err != nil {
		panic(fmt.Errorf("marshal event proto: %w", err))
	}

	b.count.Add(1)
	return true
}

func initTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string) (_ *websocket.Conn, finalErr error) {
	slog.Debug("initializing tunnel session", "tunnelID", tunnelID, "role", tunnel.Role_INSERT)

	conn, resp, err := websocket.Dial(ctx, endpoint, nil)
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

	defer func() {
		if finalErr != nil {
			conn.Close(websocket.StatusInternalError, finalErr.Error())
		}
	}()

	b, err := proto.Marshal(&tunnel.ClientHello{EventFields: tunnel.EventFields})
	if err != nil {
		return nil, fmt.Errorf("client hello: marshal: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		return nil, fmt.Errorf("client hello: write: %w", err)
	}

	var serverHello tunnel.ServerHello
	typ, b, err := conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("server hello: read: %w", err)
	}
	if typ != websocket.MessageBinary {
		return nil, fmt.Errorf("server hello: unexpected websocket message type %d", typ)
	}
	if err := proto.Unmarshal(b, &serverHello); err != nil {
		return nil, fmt.Errorf("server hello: unmarshal: %w", err)
	}

	return conn, nil
}

func doInsert(ctx context.Context, conn *websocket.Conn, data []byte) error {
	// TODO(adtac): it's wasteful to re-encode and copy the data into yet another
	// byte buffer (similarly for the read). Consider using protodelim instead?
	tunnelQueryID := uuid.New()
	b, err := proto.Marshal(&tunnel.Query{
		TunnelQueryId: tunnelQueryID.String(),
		SqlStatement:  `INSERT INTO events FORMAT Protobuf`,
		Data:          data,
	})
	if err != nil {
		return fmt.Errorf("query: marshal: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		return fmt.Errorf("query: write: %w", err)
	}

	var result tunnel.Result
	typ, b, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("result: read: %w", err)
	}
	if typ != websocket.MessageBinary {
		return fmt.Errorf("result: unexpected websocket message type %d", typ)
	}
	if err := proto.Unmarshal(b, &result); err != nil {
		return fmt.Errorf("result: unmarshal: %w", err)
	}

	if result.TunnelQueryId != tunnelQueryID.String() {
		// TODO(adtac): This shouldn't really be an error. We're opening a new
		// tunnel session for every INSERT, which is wasteful. When we start
		// reusing the same tunnel for multiple queries, there needs to be a tunnel
		// manager that routes results to the right query based on the query ID.
		return fmt.Errorf("got result query ID %s, want %s", result.TunnelQueryId, tunnelQueryID.String())
	}

	if result.TunnelError != "" {
		return fmt.Errorf("tunnel error: %s", result.TunnelError)
	}

	if result.ClickhouseError != "" {
		return fmt.Errorf("clickhouse error: %s", result.ClickhouseError)
	}

	return nil
}

func (b *block) flush(ctx context.Context) error {
	b.open.Store(false)
	b.wg.Wait()

	data := b.buf[:b.off.Load()]
	if len(data) == 0 {
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

	if err := doInsert(ctx, conn, data); err != nil {
		return fmt.Errorf("do insert: %w", err)
	}

	slog.Debug("flushed data to clickhouse", "events", b.count.Load(), "size", len(data), "took", time.Since(start).Round(time.Millisecond))
	return nil
}

type Manager struct {
	pool sync.Pool
	cur  atomic.Pointer[block]
}

func newManager() *Manager {
	m := &Manager{pool: sync.Pool{New: func() any { return new(block) }}}
	b := m.pool.Get().(*block)
	b.reset()
	m.cur.Store(b)
	return m
}

func (m *Manager) Insert(ev *event.Event) {
	var next *block
	for {
		cur := m.cur.Load()
		if cur.insert(ev) {
			slog.Debug("inserted new event", slog.Group("event", "id", ev.EventId, "type", ev.Type))
			if next != nil {
				m.pool.Put(next)
			}
			return
		}

		if next == nil {
			next = m.pool.Get().(*block)
			next.reset()
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
	next.reset()
	for {
		cur := m.cur.Load()
		if m.cur.CompareAndSwap(cur, next) {
			err := cur.flush(context.TODO())
			m.pool.Put(cur)
			return err
		}
	}
}

var DefaultManager = newManager()
