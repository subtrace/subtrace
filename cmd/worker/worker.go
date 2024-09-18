// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package worker

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
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
	"text/template"
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
	c.clickhouse = client

	if val := os.Getenv("SUBTRACE_CLICKHOUSE_SEED_NAMESPACE"); val != "" {
		namespaceID, err := uuid.Parse(val)
		if err != nil {
			return fmt.Errorf("parse SUBTRACE_CLICKHOUSE_SEED_NAMESPACE as UUID: %w", err)
		}

		suffix := getSuffixForNamespace(namespaceID)
		if applied, err := client.ApplyMigrations(ctx, suffix); err != nil {
			return fmt.Errorf("apply migrations for seed namespace: %w", err)
		} else if applied > 0 {
			slog.Info(fmt.Sprintf("applied %d new clickhouse migrations in seed namespace", applied))
		} else {
			slog.Info("no new clickhouse migrations for seed namespace")
		}

		tc := &tunnelConn{worker: c, namespaceID: namespaceID}
		var count uint64
		if err := c.clickhouse.QueryRow(ctx, fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s.%s;
		`, tc.database(), tc.table())).Scan(&count); err != nil {
			return fmt.Errorf("seed clickhouse: SELECT %s: %w", tc.table(), err)
		}

		if count == 0 {
			var columns []string
			for column := range event.KnownFields_value {
				columns = append(columns, column)
			}

			slog.Info("clickhouse seed: creating columns", "knownFields", len(columns))
			if err := tc.ensureClickhouseColumns(ctx, columns); err != nil {
				return fmt.Errorf("seed clickhouse: add columns: %w", err)
			}

			tmpl, err := template.New("seed.sql").Parse(seed)
			if err != nil {
				return fmt.Errorf("seed clickhouse: parse template: seed.sql: %w", err)
			}

			out := new(bytes.Buffer)
			if err := tmpl.Execute(out, map[string]string{"Suffix": suffix}); err != nil {
				return fmt.Errorf("seed clickhouse: execute template: seed.sql: %w", err)
			}

			slog.Info("clickhouse seed: inserting data")
			if err := c.clickhouse.Exec(ctx, out.String()); err != nil {
				return fmt.Errorf("seed clickhouse: insert data: %w", err)
			}
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

			namespaceID, err := uuid.Parse(tunnels[i].NamespaceId)
			if err != nil {
				return fmt.Errorf("invalid namespace ID: parse as UUID: %w", err)
			}

			if _, ok := known[tunnelID]; !ok {
				known[tunnelID] = struct{}{}
				go func(i int) {
					start := time.Now()
					slog.Debug("initializing new tunnel", "tunnelID", tunnelID, "role", tunnels[i].Role, "namespaceID", namespaceID)
					defer func() { slog.Debug("finished handling tunnel", "tunnelID", tunnelID, "time", time.Since(start)) }()

					tc, err := c.newTunnelConn(ctx, tunnelID, tunnels[i].Endpoint, tunnels[i].Role, namespaceID)
					if err != nil {
						var wsErr websocket.CloseError
						if errors.As(err, &wsErr) && wsErr.Code == websocket.StatusGoingAway {
							// StatusGoingAway means the tunnel proxy timed out waiting for the
							// source side to join. This shouldn't happen often, but even if it does,
							// it's not really an error from the worker's point of view, hence the
							// INFO-level log message.
							slog.Info("tunnel timed out waiting for source side", "tunnelID", tunnelID, "time", time.Since(start))
							return
						}
						slog.Error("failed to create tunnel conn", "tunnelID", tunnelID, "err", err)
						return
					}
					defer tc.Close()

					if err := tc.loop(ctx); err != nil && !strings.Contains(err.Error(), "StatusNormalClosure") {
						slog.Error("failed to loop tunnel", "tunnelID", tunnelID, "err", err)
						return
					}
				}(i)
			}
		}

		after = now
	}
}

func getSuffixForNamespace(namespaceID uuid.UUID) string {
	return strings.ReplaceAll(namespaceID.String(), "-", "")
}

type tunnelConn struct {
	tunnelID    uuid.UUID
	role        tunnel.Role
	namespaceID uuid.UUID
	websocket   *websocket.Conn
	worker      *Command
}

func (c *Command) newTunnelConn(ctx context.Context, tunnelID uuid.UUID, endpoint string, role tunnel.Role, namespaceID uuid.UUID) (_ *tunnelConn, finalErr error) {
	tc := &tunnelConn{
		tunnelID:    tunnelID,
		role:        role,
		namespaceID: namespaceID,
		worker:      c,
	}

	applied, err := c.clickhouse.ApplyMigrations(ctx, tc.suffix())
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	if applied > 0 {
		slog.Info(fmt.Sprintf("applied %d new clickhouse migrations", applied))
	} else {
		slog.Info("no new clickhouse migrations")
	}

	ws, resp, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		if ws != nil {
			ws.CloseNow()
		}
		err := fmt.Errorf("bad response status: got %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
		if resp.Body != nil {
			if b, _ := io.ReadAll(resp.Body); len(b) > 0 {
				err = fmt.Errorf("%w: %s", err, string(b))
			}
		}
		return nil, err
	}
	tc.websocket = ws

	return tc, nil
}

func (tc *tunnelConn) Close() error {
	return tc.websocket.Close(websocket.StatusNormalClosure, "")
}

func (tc *tunnelConn) suffix() string {
	return getSuffixForNamespace(tc.namespaceID)
}

func (tc *tunnelConn) database() string {
	return tc.worker.flags.clickhouse.database
}

func (tc *tunnelConn) table() string {
	return fmt.Sprintf("subtrace_events_%s", tc.suffix())
}

func (tc *tunnelConn) hasColumn(ctx context.Context, column string) (bool, error) {
	// TODO: cache the list of known columns
	var count uint64
	if err := tc.worker.clickhouse.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM system.columns
		WHERE database = $1 AND table = $2 AND name = $3;
	`, tc.database(), tc.table(), column).Scan(&count); err != nil {
		return false, fmt.Errorf("hasColumn: SELECT COUNT(*): %w", err)
	}
	return count > 0, nil
}

func (tc *tunnelConn) ensureClickhouseColumns(ctx context.Context, columns []string) error {
	var missing []string
	for i := range columns {
		exists, err := tc.hasColumn(ctx, columns[i])
		if err != nil {
			return fmt.Errorf("check if column exists: %w", err)
		}
		if !exists {
			missing = append(missing, columns[i])
			continue
		}
	}

	if len(missing) > 0 {
		if err := tc.addClickhouseColumns(ctx, missing); err != nil {
			return fmt.Errorf("add %d columns: %w", len(missing), err)
		}
	}
	return nil
}

func (tc *tunnelConn) rebuildMaterializedView(ctx context.Context) error {
	rows, err := tc.worker.clickhouse.Query(ctx, `
		SELECT name
		FROM system.columns
		WHERE database = $1 AND table = $2;
	`, tc.database(), tc.table())
	if err != nil {
		return fmt.Errorf("SELECT system.columns: %w", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return fmt.Errorf("scan system.columns: %w", err)
		}

		switch column {
		case "time":
			cols = append(cols, "coalesce(parseDateTime64BestEffort(tags['time'], 9, 'UTC'), now64(9)) AS `time`")
		case "event_id":
			cols = append(cols, "coalesce(toUUIDOrNull(tags['event_id']), generateUUIDv4()) AS `event_id`")
		default:
			cols = append(cols, fmt.Sprintf("tags['%s'] AS `%s`", column, column))
		}
	}

	if err := tc.worker.clickhouse.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s.subtrace_ingest_mv_%s MODIFY QUERY
		SELECT %s
		FROM (SELECT mapReverseSort(extractKeyValuePairsWithEscaping(line, '=', ' ', '"')) AS tags FROM subtrace_ingest_%s)
	`, tc.database(), tc.suffix(), strings.Join(cols, ", "), tc.suffix())); err != nil {
		return fmt.Errorf("ALTER TABLE: %w", err)
	}
	return nil
}

func (tc *tunnelConn) addClickhouseColumns(ctx context.Context, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	slog.Info("adding new clickhouse columns", "columns", len(columns))

	var add []string
	for i := range columns {
		switch columns[i] {
		case "http_duration":
			add = append(add, fmt.Sprintf("ADD COLUMN IF NOT EXISTS `%s` Nullable(UInt64)", columns[i]))
		default:
			add = append(add, fmt.Sprintf("ADD COLUMN IF NOT EXISTS `%s` Nullable(String) CODEC(ZSTD(1))", columns[i]))
			add = append(add, fmt.Sprintf("ADD INDEX IF NOT EXISTS `subtrace_index_%s_%s` `%s` TYPE bloom_filter(0.01) GRANULARITY 1", tc.suffix(), columns[i], columns[i]))
		}
	}

	if err := tc.worker.clickhouse.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s.%s %s
	`, tc.database(), tc.table(), strings.Join(add, ", "))); err != nil {
		return fmt.Errorf("ALTER TABLE: %w", err)
	}

	if err := tc.rebuildMaterializedView(ctx); err != nil {
		return fmt.Errorf("rebuild materialized view: %w", err)
	}
	return nil
}

func (tc *tunnelConn) loop(ctx context.Context) error {
	slog.Debug("starting tunnel loop", "tunnelID", tc.tunnelID)

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

		typ, msg, err := tc.websocket.Read(ctx)
		if err != nil {
			return fmt.Errorf("query: read: %w", err)
		}
		if typ != websocket.MessageBinary {
			return fmt.Errorf("query: unexpected websocket message type %d", typ)
		}

		// TODO: enforce sane limits on the maximum number of concurrent queries?
		go func() {
			if err := tc.handleQuery(childCtx, msg); err != nil {
				slog.Error("failed to handle query", "tunnelID", tc.tunnelID, "err", err)
				return
			}
		}()
	}
}

func (tc *tunnelConn) handleQuery(ctx context.Context, msg []byte) error {
	var result *tunnel.Result
	switch tc.role {
	case tunnel.Role_INSERT:
		q := new(tunnel.Insert)
		if err := proto.Unmarshal(msg, q); err != nil {
			return fmt.Errorf("unmarshal proto as Insert: %w", err)
		}
		result = tc.handleInsert(ctx, q)

	case tunnel.Role_SELECT:
		q := new(tunnel.Select)
		if err := proto.Unmarshal(msg, q); err != nil {
			return fmt.Errorf("unmarshal proto as Select: %w", err)
		}
		result = tc.handleSelect(ctx, q)
	}

	b, err := proto.Marshal(result)
	if err != nil {
		return fmt.Errorf("result: marshal: %w", err)
	}
	if err := tc.websocket.Write(ctx, websocket.MessageBinary, b); err != nil {
		return fmt.Errorf("result: write: %w", err)
	}
	return nil
}

func (tc *tunnelConn) handleInsert(ctx context.Context, q *tunnel.Insert) *tunnel.Result {
	seen := make(map[string]bool)
	re := regexp.MustCompile(`[a-zA-Z0-9_]+=`)
	for i := range q.Events {
		for _, column := range re.FindAllString(q.Events[i], -1) {
			seen[column[:len(column)-1]] = true
		}
	}

	var columns []string
	for column := range seen {
		columns = append(columns, column)
	}

	slog.Debug("ensuring columns exist before INSERT", "tunnelID", tc.tunnelID, "queryID", q.TunnelQueryId, "columns", len(columns))
	if err := tc.ensureClickhouseColumns(ctx, columns); err != nil {
		return &tunnel.Result{
			TunnelQueryId: q.TunnelQueryId,
			TunnelError:   fmt.Errorf("ensure columns: %w", err).Error(),
		}
	}

	data, err := json.Marshal(map[string][]string{"line": q.Events})
	if err != nil {
		return &tunnel.Result{
			TunnelQueryId: q.TunnelQueryId,
			TunnelError:   fmt.Errorf("encode JSONColumns: marshal: %w", err).Error(),
		}
	}

	return tc.runQuery(ctx, q.TunnelQueryId, fmt.Sprintf(`
		INSERT INTO subtrace_ingest_%s FORMAT JSONColumns
	`, tc.suffix()), bytes.NewBuffer(data))
}

func (tc *tunnelConn) handleSelect(ctx context.Context, q *tunnel.Select) *tunnel.Result {
	return tc.runQuery(ctx, q.TunnelQueryId, q.SqlStatement, nil)
}

func (tc *tunnelConn) runQuery(ctx context.Context, tunnelQueryID string, query string, data io.Reader) *tunnel.Result {
	result, clickhouseQueryID, clickhouseErr, tunnelErr := tc.proxyClickhouse(ctx, query, data)
	if tunnelErr != nil {
		return &tunnel.Result{
			TunnelQueryId:     tunnelQueryID,
			ClickhouseQueryId: clickhouseQueryID,
			TunnelError:       tunnelErr.Error(),
		}
	}
	if clickhouseErr != nil {
		return &tunnel.Result{
			TunnelQueryId:     tunnelQueryID,
			ClickhouseQueryId: clickhouseQueryID,
			ClickhouseError:   clickhouseErr.Error(),
		}
	}
	return &tunnel.Result{
		TunnelQueryId:     tunnelQueryID,
		ClickhouseQueryId: clickhouseQueryID,
		Result:            result,
	}
}

func (tc *tunnelConn) proxyClickhouse(ctx context.Context, query string, data io.Reader) (_ string, clickhouseQueryID string, clickhouseErr error, tunnelErr error) {
	const maxResultBytes = 64 << 20

	// 8123 is the ClickHouse HTTP default port
	// ref: https://clickhouse.com/docs/en/guides/sre/network-ports
	u, err := url.Parse(fmt.Sprintf("http://%s:8123/", tc.worker.flags.clickhouse.host))
	if err != nil {
		return "", "", nil, fmt.Errorf("parse clickhouse HTTP interface endpoint: %w", err)
	}

	q := u.Query()
	q.Set("query", query)
	q.Set("max_result_bytes", fmt.Sprintf("%d", maxResultBytes))
	q.Set("buffer_size", fmt.Sprintf("%d", maxResultBytes))
	q.Set("wait_end_of_query", "1")
	u.RawQuery = q.Encode()

	// TODO: set a timeout for each query (maybe makes sense to have different
	// timeouts for SELECTs and INSERTs)?
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), data)
	if err != nil {
		return "", "", nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("x-clickhouse-database", tc.database())
	req.Header.Set("x-clickhouse-format", "JSONCompact")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(bufio.NewReader(resp.Body), maxResultBytes)

	clickhouseQueryID = resp.Header.Get("x-clickhouse-query-id")

	if resp.StatusCode != http.StatusOK {
		clickhouseErr = fmt.Errorf("clickhouse returned status %d", resp.StatusCode)
		if code := resp.Header.Get("x-clickhouse-exception-code"); code != "" {
			clickhouseErr = fmt.Errorf("%w: exception %s", clickhouseErr, code)
		}
		var details struct {
			Exception *string `json:"exception"`
		}
		if err := json.NewDecoder(body).Decode(&details); err == nil && details.Exception != nil {
			clickhouseErr = fmt.Errorf("%w (details: %s)", clickhouseErr, *details.Exception)
		}
		return "", clickhouseQueryID, clickhouseErr, nil
	}

	result, err := io.ReadAll(body)
	if err != nil {
		return "", clickhouseQueryID, nil, fmt.Errorf("read clickhouse response body: %w", err)
	}
	return string(result), clickhouseQueryID, nil, nil
}
