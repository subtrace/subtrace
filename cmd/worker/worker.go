// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"nhooyr.io/websocket"
	"subtrace.dev/cmd/worker/clickhouse"
	"subtrace.dev/logging"
	"subtrace.dev/rpc"
	"subtrace.dev/tunnel"
)

type Command struct {
	flags struct {
		clickhouse struct {
			host          string
			formatSchemas string
		}
	}

	ffcli.Command

	clickhouse *clickhouse.Client
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "worker"
	c.ShortUsage = "subtrace worker [flags]"
	c.ShortHelp = "start a worker node"

	c.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	c.FlagSet.BoolVar(&logging.Verbose, "v", false, "enable verbose logging")
	c.FlagSet.StringVar(&c.flags.clickhouse.host, "clickhouse-host", "localhost", "clickhouse host")
	c.FlagSet.StringVar(&c.flags.clickhouse.formatSchemas, "clickhouse-format-schemas", "/var/lib/clickhouse/format_schemas/", "clickhouse format schemas directory")

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE")}
	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()

	if val := os.Getenv("SUBTRACE_TOKEN"); val == "" {
		return fmt.Errorf("SUBTRACE_TOKEN is empty")
	}

	slog.Info("starting worker node")

	if err := c.initClickhouse(ctx); err != nil {
		return fmt.Errorf("init clickhouse: %w", err)
	}
	defer c.clickhouse.Close()

	if err := os.MkdirAll(c.flags.clickhouse.formatSchemas, 0o755); err != nil {
		return fmt.Errorf("create clickhouse format schemas directory: %w", err)
	}

	if err := c.loop(ctx); err != nil {
		return fmt.Errorf("upload handler loop: %w", err)
	}

	return nil
}

func (c *Command) initClickhouse(ctx context.Context) error {
	client, err := clickhouse.New(ctx, c.flags.clickhouse.host)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	old, applied, err := client.ApplyMigrations(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if applied > 0 {
		slog.Info(fmt.Sprintf("applied %d new clickhouse migrations", applied))
	} else {
		slog.Info("no new clickhouse migrations")
	}

	c.clickhouse = client

	switch strings.ToLower(os.Getenv("SUBTRACE_SEED_DATABASE")) {
	case "y", "yes", "true", "t", "1":
		if old == 0 {
			if err := c.addClickhouseColumns(ctx, tunnel.EventFields); err != nil {
				return fmt.Errorf("seed clickhouse: add columns: %w", err)
			}
			if err := c.seedClickhouseSampleData(ctx); err != nil {
				return fmt.Errorf("seed clickhouse: insert sample data: %w", err)
			}
		}
	}
	return nil
}

func (c *Command) loop(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	first := make(chan struct{}, 1)
	first <- struct{}{}

	known := make(map[uuid.UUID]struct{})

	var after time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-first:
		}

		now := time.Now().UTC()
		var tunnels []*tunnel.ListOpen_Item
		for wait := time.Second; ; wait *= 2 {
			var resp tunnel.ListOpen_Response
			_, err := rpc.Call(ctx, &resp, "/api/ListOpenTunnels", &tunnel.ListOpen_Request{CreateAfterTime: after.UnixNano()})
			if err == nil {
				tunnels = resp.Tunnels
				break
			}
			if wait > 30*time.Second {
				return fmt.Errorf("list open tunnels: %w", err)
			}
			slog.Error("ListOpenTunnels failed, retrying after exponential backoff", "err", err, "wait", wait)
			time.Sleep(wait) // TODO: jitter
		}

		for i := range tunnels {
			tunnelID, err := uuid.Parse(tunnels[i].TunnelId)
			if err != nil {
				return fmt.Errorf("invalid tunnel ID: parse as UUID: %w", err)
			}

			if _, ok := known[tunnelID]; !ok {
				known[tunnelID] = struct{}{}
				go func(i int) {
					if err := c.handleTunnel(ctx, tunnelID, tunnels[i].Endpoint, tunnels[i].Role); err != nil {
						slog.Error("handle tunnel", "tunnelID", tunnelID, "err", err)
						return
					}
				}(i)
			}
		}

		after = now
	}
}

func (c *Command) handleTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string, role tunnel.Role) error {
	start := time.Now()
	defer func() { slog.Debug("closing tunnel", "tunnelID", tunnelID, "time", time.Since(start)) }()

	conn, path, err := c.initTunnel(ctx, tunnelID, endpoint, role)
	if err != nil {
		var wsErr websocket.CloseError
		if errors.As(err, &wsErr) && wsErr.Code == websocket.StatusGoingAway {
			// StatusGoingAway means the tunnel proxy timed out waiting for the
			// source side to join. This shouldn't happen often, but even if it does,
			// it's not really an error from the worker's point of view, hence the
			// INFO-level log message.
			slog.Info("tunnel timed out waiting for source", "tunnelID", tunnelID, "time", time.Since(start))
			return nil
		}
		return fmt.Errorf("init tunnel: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	defer os.Remove(path)

	if err := c.loopTunnel(ctx, tunnelID, conn, role); err != nil && !strings.Contains(err.Error(), "StatusNormalClosure") {
		return fmt.Errorf("loop tunnel: %w", err)
	}
	return nil
}

func (c *Command) initTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string, role tunnel.Role) (_ *websocket.Conn, _ string, finalErr error) {
	slog.Debug("initializing tunnel session", "tunnelID", tunnelID, "role", role)

	conn, resp, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("dial: %w", err)
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
		return nil, "", err
	}

	defer func() {
		if finalErr != nil {
			conn.Close(websocket.StatusInternalError, finalErr.Error())
		}
	}()

	var clientHello tunnel.ClientHello
	typ, b, err := conn.Read(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("client hello: read: %w", err)
	}
	if typ != websocket.MessageBinary {
		return nil, "", fmt.Errorf("client hello: unexpected websocket message type %d", typ)
	}
	if err := proto.Unmarshal(b, &clientHello); err != nil {
		return nil, "", fmt.Errorf("client hello: unmarshal: %w", err)
	}

	switch role {
	case tunnel.Role_INSERT:
		if err := c.ensureClickhouseColumns(ctx, clientHello.EventFields); err != nil {
			return nil, "", fmt.Errorf("client hello: ensure event columns: %w", err)
		}
	case tunnel.Role_SELECT:
	default:
		return nil, "", fmt.Errorf("client hello: unknown role: %w", err)
	}

	path, err := c.writeTunnelProto(ctx, tunnelID, clientHello.EventFields)
	if err != nil {
		return nil, "", fmt.Errorf("client hello: write tunnel protobuf schema: %w", err)
	}
	defer func() {
		if finalErr != nil {
			os.Remove(path)
		}
	}()

	b, err = proto.Marshal(&tunnel.ServerHello{})
	if err != nil {
		return nil, path, fmt.Errorf("server hello: marshal: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		return nil, path, fmt.Errorf("server hello: write: %w", err)
	}

	return conn, path, nil
}

func (c *Command) hasColumn(ctx context.Context, name string) (bool, error) {
	// TODO: cache the list of known columns
	var count uint64
	if err := c.clickhouse.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM system.columns
		WHERE database = 'subtrace' AND table = 'events' AND name = $1;
	`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("SELECT system.columns: %w", err)
	}
	return count > 0, nil
}

func (c *Command) ensureClickhouseColumns(ctx context.Context, fields []*tunnel.EventField) error {
	var add []*tunnel.EventField
	for _, f := range fields {
		exists, err := c.hasColumn(ctx, fmt.Sprintf("T%08x", f.Tag))
		if err != nil {
			return fmt.Errorf("check if column exists: %w", err)
		}
		if !exists {
			add = append(add, f)
			continue
		}
	}

	if len(add) > 0 {
		sort.Slice(fields, func(i, j int) bool { return fields[i].Tag < fields[j].Tag })
		if err := c.addClickhouseColumns(ctx, add); err != nil {
			return fmt.Errorf("add %d columns: %w", len(add), err)
		}
	}
	return nil
}

func (c *Command) addClickhouseColumns(ctx context.Context, fields []*tunnel.EventField) error {
	if len(fields) == 0 {
		return nil
	}

	var errs []error
	for _, f := range fields {
		var typ string
		switch protoreflect.Kind(f.Type) {
		case protoreflect.BoolKind:
			typ = "Bool"
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			typ = "Int32"
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			typ = "UInt32"
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			typ = "Int64"
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			typ = "UInt64"
		case protoreflect.StringKind:
			typ = "String"
		default:
			return fmt.Errorf("tag %d: unknown type %q", f.Tag, f.Type)
		}
		if err := c.clickhouse.Exec(ctx, fmt.Sprintf(`
			ALTER TABLE subtrace.events
			ADD COLUMN IF NOT EXISTS T%08x %s
		`, f.Tag, typ)); err != nil {
			errs = append(errs, fmt.Errorf("add field %d (%s): %w", f.Tag, protoreflect.Kind(f.Type).String(), err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("ALTER TABLE ADD COLUMN: %w", err)
	}

	// Without dropping the format schema cache explicitly, Clickhouse ignores
	// newly fields when doing INSERTs even if the column exists.
	if err := c.clickhouse.Exec(ctx, `
		SYSTEM DROP FORMAT SCHEMA CACHE
	`); err != nil {
		return fmt.Errorf("DROP FORMAT SCHEMA CACHE: %w", err)
	}
	return nil
}

func (c *Command) writeTunnelProto(ctx context.Context, tunnelID uuid.UUID, fields []*tunnel.EventField) (string, error) {
	path := filepath.Join(c.flags.clickhouse.formatSchemas, fmt.Sprintf("%s.proto", tunnelID))

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "syntax = \"proto3\";\n")
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "message Event {\n")
	for _, f := range fields {
		prefix := "  optional "
		if f.Tag == 1 || f.Tag == 2 || f.Tag == 3 {
			prefix = "  " // event_id, timestamp, type
		}
		fmt.Fprintf(w, "%s%s T%08x = %d;\n", prefix, protoreflect.Kind(f.Type).String(), f.Tag, f.Tag)
	}
	fmt.Fprintf(w, "}\n")

	return path, nil
}

func (c *Command) loopTunnel(ctx context.Context, tunnelID uuid.UUID, conn *websocket.Conn, role tunnel.Role) error {
	slog.Debug("starting tunnel loop", "tunnelID", tunnelID)

	var mu sync.Mutex
	var prevCancel context.CancelFunc
	defer func() {
		if prevCancel != nil {
			prevCancel()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		var query tunnel.Query
		typ, b, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("query: read: %w", err)
		}
		if typ != websocket.MessageBinary {
			return fmt.Errorf("query: unexpected websocket message type %d", typ)
		}
		if err := proto.Unmarshal(b, &query); err != nil {
			return fmt.Errorf("query: unmarshal: %w", err)
		}

		if !mu.TryLock() {
			// There can only be one inflight query at any time, we must cancel any
			// previous queries that are still running. If this is the first query or
			// if the previous query has already finished, this is a noop.
			if prevCancel != nil {
				prevCancel()
			}

			// Now wait for the previous query to release the lock.
			mu.Lock()
		}

		childCtx, cancel := context.WithCancel(ctx)
		prevCancel = cancel

		go func() {
			defer mu.Unlock()
			if err := c.handleQuery(childCtx, tunnelID, conn, role, &query); err != nil {
				slog.Error("failed to handle query", "tunnelID", tunnelID, "queryID", query.TunnelQueryId, "err", err)
				return
			}
		}()
	}
}

func (c *Command) handleQuery(ctx context.Context, tunnelID uuid.UUID, conn *websocket.Conn, role tunnel.Role, query *tunnel.Query) error {
	result := &tunnel.Result{TunnelQueryId: query.TunnelQueryId}

	status, headers, data, err := c.proxyClickhouse(ctx, tunnelID, role, query)
	switch {
	case err != nil:
		result.TunnelError = fmt.Errorf("failed to proxy query to clickhouse: %w", err).Error()
	case status == http.StatusOK:
		result.ClickhouseQueryId = headers.Get("x-clickhouse-query-id")
		result.Data = data
	default:
		err := fmt.Errorf("clickhouse returned status %d", status)
		if len(data) != 0 {
			err = fmt.Errorf("%w: %s", err, string(data))
		} else if exception := headers.Get("x-clickhouse-exception-code"); exception != "" {
			err = fmt.Errorf("%w: clickhouse exception code %s", err, exception)
		} else {
			err = fmt.Errorf("%w: <empty error message>", err)
		}
		result.ClickhouseQueryId = headers.Get("x-clickhouse-query-id")
		result.ClickhouseError = err.Error()
	}

	b, err := proto.Marshal(result)
	if err != nil {
		return fmt.Errorf("result: marshal: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		return fmt.Errorf("result: write: %w", err)
	}
	return nil
}

func (c *Command) proxyClickhouse(ctx context.Context, tunnelID uuid.UUID, role tunnel.Role, query *tunnel.Query) (int, http.Header, []byte, error) {
	const maxResultBytes = 1 << 20

	// 8123 is the ClickHouse HTTP default port
	// ref: https://clickhouse.com/docs/en/guides/sre/network-ports
	u, err := url.Parse(fmt.Sprintf("http://%s:8123/", c.flags.clickhouse.host))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("parse clickhouse HTTP interface endpoint: %w", err)
	}

	q := u.Query()
	q.Set("query", query.SqlStatement)
	q.Set("format_schema", fmt.Sprintf("%s:Event", tunnelID.String()))
	q.Set("max_result_bytes", fmt.Sprintf("%d", maxResultBytes))
	q.Set("buffer_size", fmt.Sprintf("%d", maxResultBytes))
	q.Set("wait_end_of_query", "1")
	switch role {
	case tunnel.Role_INSERT:
		// TODO: q.Set("role", "subtrace_tunnel_insert")
	case tunnel.Role_SELECT:
		// TODO: q.Set("role", "subtrace_tunnel_select")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewBuffer(query.Data))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("x-clickhouse-database", "subtrace")
	req.Header.Set("x-clickhouse-format", "Protobuf")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(bufio.NewReader(resp.Body), maxResultBytes))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, resp.Header, b, nil
}
