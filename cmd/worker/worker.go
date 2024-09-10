// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package worker

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
	"subtrace.dev/cmd/version"
	"subtrace.dev/cmd/worker/clickhouse"
	"subtrace.dev/event"
	"subtrace.dev/logging"
	"subtrace.dev/rpc"
	"subtrace.dev/tunnel"
)

type Command struct {
	flags struct {
		clickhouse struct {
			host     string
			database string
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
	c.FlagSet.StringVar(&c.flags.clickhouse.database, "clickhouse-database", "subtrace", "clickhouse database")

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE")}
	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()

	if val := os.Getenv("SUBTRACE_TOKEN"); val == "" {
		return fmt.Errorf("SUBTRACE_TOKEN is empty")
	}

	slog.Info("starting worker node", "release", version.Release, slog.Group("commit", "hash", version.CommitHash, "time", version.CommitTime), "build", version.BuildTime)

	if err := c.initClickhouse(ctx); err != nil {
		return fmt.Errorf("init clickhouse: %w", err)
	}
	defer c.clickhouse.Close()

	if err := c.loop(ctx); err != nil {
		return fmt.Errorf("tunnel handler loop: %w", err)
	}

	return nil
}

//go:embed seed.sql
var seed string

func (c *Command) initClickhouse(ctx context.Context) error {
	client, err := clickhouse.New(ctx, c.flags.clickhouse.host, c.flags.clickhouse.database)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	applied, err := client.ApplyMigrations(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if applied > 0 {
		slog.Info(fmt.Sprintf("applied %d new clickhouse migrations", applied))
	} else {
		slog.Info("no new clickhouse migrations")
	}

	c.clickhouse = client

	switch strings.ToLower(os.Getenv("SUBTRACE_CLICKHOUSE_SEED_DATA")) {
	case "y", "yes", "true", "t", "1":
		var count uint64
		if err := c.clickhouse.QueryRow(ctx, fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s.events;
		`, c.flags.clickhouse.database)).Scan(&count); err != nil {
			return fmt.Errorf("seed clickhouse: SELECT events: %w", err)
		}

		if count == 0 {
			slog.Info("seeding clickhouse events table with sample data")
			var cols []string
			for col := range event.KnownFields_value {
				cols = append(cols, col)
			}
			if err := c.ensureClickhouseColumns(ctx, cols); err != nil {
				return fmt.Errorf("seed clickhouse: add columns: %w", err)
			}
			if err := c.clickhouse.Exec(ctx, seed); err != nil {
				return fmt.Errorf("seed clickhouse: insert data: %w", err)
			}
			slog.Info("seeded clickhouse events table with sample data")
		}
	}
	return nil
}

func (c *Command) loop(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
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

	conn, err := c.initTunnel(ctx, tunnelID, endpoint, role)
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

	if err := c.loopTunnel(ctx, tunnelID, conn, role); err != nil && !strings.Contains(err.Error(), "StatusNormalClosure") {
		return fmt.Errorf("loop tunnel: %w", err)
	}
	return nil
}

func (c *Command) initTunnel(ctx context.Context, tunnelID uuid.UUID, endpoint string, role tunnel.Role) (_ *websocket.Conn, finalErr error) {
	slog.Debug("initializing tunnel session", "tunnelID", tunnelID, "role", role)

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

	switch role {
	case tunnel.Role_INSERT:
		// TODO: checks
	case tunnel.Role_SELECT:
		// TODO: checks
	default:
		return nil, fmt.Errorf("client hello: unknown role: %w", err)
	}

	return conn, nil
}

func (c *Command) hasColumn(ctx context.Context, name string) (bool, error) {
	// TODO: cache the list of known columns
	var count uint64
	if err := c.clickhouse.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM system.columns
		WHERE database = '%s' AND table = 'events' AND name = $1;
	`, c.flags.clickhouse.database), name).Scan(&count); err != nil {
		return false, fmt.Errorf("hasColumn: SELECT COUNT(*): %w", err)
	}
	return count > 0, nil
}

func (c *Command) ensureClickhouseColumns(ctx context.Context, cols []string) error {
	var add []string
	for _, col := range cols {
		exists, err := c.hasColumn(ctx, col)
		if err != nil {
			return fmt.Errorf("check if column exists: %w", err)
		}
		if !exists {
			add = append(add, col)
			continue
		}
	}

	if len(add) > 0 {
		if err := c.addClickhouseColumns(ctx, add); err != nil {
			return fmt.Errorf("add %d columns: %w", len(add), err)
		}
	}
	return nil
}

func (c *Command) rebuildMaterializedView(ctx context.Context) error {
	rows, err := c.clickhouse.Query(ctx, fmt.Sprintf(`
		SELECT name
		FROM system.columns
		WHERE database = '%s' AND table = 'events';
	`, c.flags.clickhouse.database))
	if err != nil {
		return fmt.Errorf("SELECT system.columns: %w", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return fmt.Errorf("scan system.columns: %w", err)
		}

		switch col {
		case "time":
			cols = append(cols, "coalesce(parseDateTime64BestEffort(tags['time'], 9, 'UTC'), now64(9)) AS `time`")
		case "event_id":
			cols = append(cols, "coalesce(toUUIDOrNull(tags['event_id']), generateUUIDv4()) AS `event_id`")
		default:
			cols = append(cols, fmt.Sprintf("tags['%s'] AS `%s`", col, col))
		}
	}

	if err := c.clickhouse.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s.ingest_mv MODIFY QUERY
		SELECT %s
		FROM (SELECT mapReverseSort(extractKeyValuePairsWithEscaping(line, '=', ' ', '"')) AS tags FROM ingest)
	`, c.flags.clickhouse.database, strings.Join(cols, ", "))); err != nil {
		return fmt.Errorf("ALTER TABLE: %w", err)
	}
	return nil
}

func (c *Command) addClickhouseColumns(ctx context.Context, cols []string) error {
	if len(cols) == 0 {
		return nil
	}

	slog.Info("adding new clickhouse columns", "columns", len(cols))

	var add []string
	for _, col := range cols {
		switch col {
		case "http_duration":
			add = append(add, fmt.Sprintf("ADD COLUMN IF NOT EXISTS `%s` Nullable(UInt64)", col))
		default:
			add = append(add, fmt.Sprintf("ADD COLUMN IF NOT EXISTS `%s` Nullable(String) CODEC(ZSTD(1))", col))
			add = append(add, fmt.Sprintf("ADD INDEX IF NOT EXISTS `index_%s` `%s` TYPE bloom_filter(0.01) GRANULARITY 1", col, col))
		}
	}

	if err := c.clickhouse.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s.events %s
	`, c.flags.clickhouse.database, strings.Join(add, ", "))); err != nil {
		return fmt.Errorf("ALTER TABLE: %w", err)
	}

	if err := c.rebuildMaterializedView(ctx); err != nil {
		return fmt.Errorf("rebuild materialized view: %w", err)
	}
	return nil
}

func (c *Command) loopTunnel(ctx context.Context, tunnelID uuid.UUID, conn *websocket.Conn, role tunnel.Role) error {
	slog.Debug("starting tunnel loop", "tunnelID", tunnelID)

	// We create a new context for the query handler so that all inflight queries
	// get killed when the tunnel closes.
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		typ, msg, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("query: read: %w", err)
		}
		if typ != websocket.MessageBinary {
			return fmt.Errorf("query: unexpected websocket message type %d", typ)
		}

		// TODO: enforce sane limits on the maximum number of concurrent queries?
		go func() {
			if err := c.handleQuery(childCtx, tunnelID, conn, role, msg); err != nil {
				slog.Error("failed to handle query", "tunnelID", tunnelID, "err", err)
				return
			}
		}()
	}
}

func (c *Command) handleQuery(ctx context.Context, tunnelID uuid.UUID, conn *websocket.Conn, role tunnel.Role, b []byte) error {
	var result *tunnel.Result
	switch role {
	case tunnel.Role_INSERT:
		query := new(tunnel.Insert)
		if err := proto.Unmarshal(b, query); err != nil {
			return fmt.Errorf("unmarshal INSERT: %w", err)
		}

		data := []string{}
		cols := make(map[string]bool)
		rexp := regexp.MustCompile(`[a-zA-Z0-9_]+=`)
		for _, event := range query.Events {
			data = append(data, fmt.Sprintf("('%s')", event))
			for _, col := range rexp.FindAllString(event, -1) {
				cols[col[:len(col)-1]] = true
			}
		}

		var ensure []string
		for col := range cols {
			ensure = append(ensure, col)
		}

		slog.Debug("ensuring columns exist before INSERT", "tunnelID", tunnelID, "queryID", query.TunnelQueryId, "columns", cols)
		if err := c.ensureClickhouseColumns(ctx, ensure); err != nil {
			result = &tunnel.Result{
				TunnelQueryId: query.TunnelQueryId,
				TunnelError:   fmt.Errorf("ensure columns: %w", err).Error(),
			}
			break
		}

		result = c.proxyClickhouse(ctx, tunnelID, query.TunnelQueryId, `INSERT INTO ingest VALUES`, bytes.NewBufferString(strings.Join(data, ",\n")))

	case tunnel.Role_SELECT:
		query := new(tunnel.Select)
		if err := proto.Unmarshal(b, query); err != nil {
			return fmt.Errorf("unmarshal SELECT: %w", err)
		}
		result = c.proxyClickhouse(ctx, tunnelID, query.TunnelQueryId, query.SqlStatement, nil)
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

func (c *Command) proxyClickhouse(ctx context.Context, tunnelID uuid.UUID, tunnelQueryID string, stmt string, data io.Reader) *tunnel.Result {
	const maxResultBytes = 64 << 20

	// 8123 is the ClickHouse HTTP default port
	// ref: https://clickhouse.com/docs/en/guides/sre/network-ports
	u, err := url.Parse(fmt.Sprintf("http://%s:8123/", c.flags.clickhouse.host))
	if err != nil {
		return &tunnel.Result{
			TunnelQueryId: tunnelQueryID,
			TunnelError:   fmt.Errorf("parse clickhouse HTTP interface endpoint: %w", err).Error(),
		}
	}

	q := u.Query()
	q.Set("query", stmt)
	q.Set("max_result_bytes", fmt.Sprintf("%d", maxResultBytes))
	q.Set("buffer_size", fmt.Sprintf("%d", maxResultBytes))
	q.Set("wait_end_of_query", "1")
	u.RawQuery = q.Encode()

	// TODO: set a timeout for each query (maybe makes sense to have different
	// timeouts for SELECTs and INSERTs)?
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), data)
	if err != nil {
		return &tunnel.Result{
			TunnelQueryId: tunnelQueryID,
			TunnelError:   fmt.Errorf("create request: %w", err).Error(),
		}
	}

	req.Header.Set("x-clickhouse-database", c.flags.clickhouse.database)
	req.Header.Set("x-clickhouse-format", "JSONCompact")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &tunnel.Result{
			TunnelQueryId: tunnelQueryID,
			TunnelError:   fmt.Errorf("do request: %w", err).Error(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("clickhouse returned status %d", resp.StatusCode)
		if msg, _ := io.ReadAll(io.LimitReader(bufio.NewReader(resp.Body), maxResultBytes)); len(msg) != 0 {
			err = fmt.Errorf("%w: %s", err, string(msg))
		} else if exception := resp.Header.Get("x-clickhouse-exception-code"); exception != "" {
			err = fmt.Errorf("%w: clickhouse exception code %s", err, exception)
		} else {
			err = fmt.Errorf("%w: <empty error message>", err)
		}
		return &tunnel.Result{
			TunnelQueryId:     tunnelQueryID,
			ClickhouseQueryId: resp.Header.Get("x-clickhouse-query-id"),
			ClickhouseError:   fmt.Errorf("do request: %w", err).Error(),
		}
	}

	result, err := io.ReadAll(io.LimitReader(bufio.NewReader(resp.Body), maxResultBytes))
	if err != nil {
		return &tunnel.Result{
			TunnelQueryId:     tunnelQueryID,
			ClickhouseQueryId: resp.Header.Get("x-clickhouse-query-id"),
			TunnelError:       fmt.Errorf("read result body: %w", err).Error(),
		}
	}

	return &tunnel.Result{
		TunnelQueryId: tunnelQueryID,
		Result:        string(result),
	}
}
