// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package clickhouse

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"text/template"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Client struct {
	driver.Conn
	database string
}

func newClient(ctx context.Context, host string, database string) (*Client, error) {
	// 9000 is the ClickHouse native protocol port
	// ref: https://clickhouse.com/docs/en/guides/sre/network-ports
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{fmt.Sprintf("%s:9000", host)},
		Auth:        clickhouse.Auth{Database: database},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}

	info, err := conn.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("get server version: %w", err)
	}

	slog.Info("connected to clickhouse server", "name", info.DisplayName, "version", info.Version, "revision", info.Revision)
	return &Client{Conn: conn, database: database}, nil
}

func New(ctx context.Context, host string, database string) (*Client, error) {
	var lastErr error
	for attempt := 1; attempt < 10; attempt++ {
		c, err := newClient(ctx, host, database)
		if err == nil {
			return c, nil
		}

		lastErr = err
		wait := min(30*time.Second, time.Duration(1<<(attempt-1))*time.Second)
		slog.Error("failed to connect to clickhouse, waiting and retrying", "host", host, "database", database, "err", err, "attempt", attempt, "wait", wait)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}

	return nil, lastErr
}

//go:embed migrations/*.sql
var migrations embed.FS

// ApplyMigrations applies all previously unapplied migrations for the given
// suffix. If successful, it returns the number of newly applied migrations.
func (c *Client) ApplyMigrations(ctx context.Context, suffix string) (applied int, _ error) {
	// Perform existence check, it's quick and avoids CH locking the database metadata if we don't
	// need to create the table (almost always the case)
	var exists bool
	if err := c.QueryRow(ctx, `
		SELECT COUNT(*) > 0
		FROM system.tables
		WHERE database = $1
		AND name = $2;
	`, c.database, fmt.Sprintf("subtrace_migrations_%s", suffix)).Scan(&exists); err != nil {
		return 0, fmt.Errorf("SELECT system.tables: subtrace_migrations_%s: %w", suffix, err)
	}

	if !exists {
		slog.Info("creating migration table if not present", "name", fmt.Sprintf("subtrace_migrations_%s", suffix))
		if err := c.Exec(ctx, fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS subtrace_migrations_%s (
				file String PRIMARY KEY,
				insert_time DateTime64(6, 'UTC') NOT NULL,
			) ENGINE = MergeTree ORDER BY file;
		`, suffix)); err != nil {
			return 0, fmt.Errorf("CREATE TABLE subtrace_migrations_%s: %w", suffix, err)
		}
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return 0, fmt.Errorf("read migrations dir: %w", err)
	}

	for _, f := range files {
		var exists bool
		if err := c.QueryRow(ctx, fmt.Sprintf(`
			SELECT true
			FROM subtrace_migrations_%s
			WHERE file = $1;
		`, suffix), f.Name()).Scan(&exists); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return applied, fmt.Errorf("SELECT subtrace_migrations_%s: %s: %w", suffix, f.Name(), err)
		}
		if exists {
			continue
		}

		slog.Info("applying new migration", "suffix", suffix, "name", f.Name())

		b, err := migrations.ReadFile(fmt.Sprintf("migrations/%s", f.Name()))
		if err != nil {
			return applied, fmt.Errorf("read template: %s: %w", f.Name(), err)
		}

		tmpl, err := template.New(f.Name()).Parse(string(b))
		if err != nil {
			return applied, fmt.Errorf("parse template: %s: %w", f.Name(), err)
		}

		out := new(bytes.Buffer)
		if err := tmpl.Execute(out, map[string]string{"Suffix": suffix}); err != nil {
			return applied, fmt.Errorf("execute template: %s: %w", f.Name(), err)
		}

		if err := c.Exec(ctx, out.String()); err != nil {
			return applied, fmt.Errorf("apply: %s: %w", f.Name(), err)
		}

		if err := c.Exec(ctx, fmt.Sprintf(`
			INSERT INTO subtrace_migrations_%s (file, insert_time)
			VALUES ($1, $2);
		`, suffix), f.Name(), time.Now().UTC()); err != nil {
			return applied, fmt.Errorf("INSERT subtrace_migrations_%s: %s: %w", suffix, f.Name(), err)
		}
		applied++
	}
	return applied, nil
}
