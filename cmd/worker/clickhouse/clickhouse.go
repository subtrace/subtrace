// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package clickhouse

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Client struct {
	driver.Conn
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
	return &Client{Conn: conn}, nil
}

func New(ctx context.Context, host string, database string) (*Client, error) {
	var lastErr error
	for attempt := 1; attempt < 5; attempt++ {
		c, err := newClient(ctx, host, database)
		if err == nil {
			return c, nil
		}

		lastErr = err
		if attempt < 5 {
			wait := time.Duration(1<<(attempt-1)) * time.Second
			slog.Error("failed to connect to clickhouse, waiting and retrying", "host", host, "database", database, "err", err, "attempt", attempt, "wait", wait)

			timer := time.NewTicker(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}

	return nil, lastErr
}

//go:embed migrations/*.sql
var migrations embed.FS

// ApplyMigrations applies all previously unapplied migrations. If successful,
// it returns the number of newly applied migrations.
func (c *Client) ApplyMigrations(ctx context.Context) (applied int, _ error) {
	if err := c.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS migrations (
			file String PRIMARY KEY,
			insert_time DateTime64(6, 'UTC') NOT NULL,
		) ENGINE = MergeTree ORDER BY file;
	`); err != nil {
		return 0, fmt.Errorf("CREATE TABLE migrations: %w", err)
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return 0, fmt.Errorf("read migrations dir: %w", err)
	}

	var count uint64
	if err := c.QueryRow(ctx, `SELECT COUNT(*) FROM migrations;`).Scan(&count); err != nil {
		return 0, fmt.Errorf("SELECT migrations: %w", err)
	}

	for _, f := range files {
		var exists bool
		if err := c.QueryRow(ctx, `
			SELECT true
			FROM migrations
			WHERE file = $1;
		`, f.Name()).Scan(&exists); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return applied, fmt.Errorf("SELECT %s: %w", f.Name(), err)
		}
		if exists {
			continue
		}

		slog.Info("applying new migration", "name", f.Name())

		b, err := migrations.ReadFile(fmt.Sprintf("migrations/%s", f.Name()))
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", f.Name(), err)
		}

		if err := c.Exec(ctx, string(b)); err != nil {
			return applied, fmt.Errorf("apply %s: %w", f.Name(), err)
		}

		if err := c.Exec(ctx, `
			INSERT INTO migrations (file, insert_time)
			VALUES ($1, $2);
		`, f.Name(), time.Now().UTC()); err != nil {
			return applied, fmt.Errorf("INSERT %s: %w", f.Name(), err)
		}
		applied++
	}

	return applied, nil
}
