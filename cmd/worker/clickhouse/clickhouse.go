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

func New(ctx context.Context) (*Client, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Auth: clickhouse.Auth{Database: "subtrace"},
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

//go:embed migrations/*.sql
var migrations embed.FS

func (c *Client) ApplyMigrations(ctx context.Context) (int, error) {
	if err := c.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS migrations (
			file String PRIMARY KEY,
			insert_time UInt64,
		) ENGINE = MergeTree PARTITION BY bitShiftRight(insert_time, 20);
	`); err != nil {
		return 0, fmt.Errorf("create migrations: %w", err)
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return 0, fmt.Errorf("list: %w", err)
	}

	applied := 0
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
			INSERT INTO
			migrations(file, insert_time)
			VALUES($1, $2);
		`, f.Name(), time.Now().UTC().UnixNano()); err != nil {
			return applied, fmt.Errorf("INSERT %s: %w", f.Name(), err)
		}
		applied++
	}

	return applied, nil
}