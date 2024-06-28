package worker

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
)

//go:embed seed.sql
var seed string

func (c *Command) seedClickhouseSampleData(ctx context.Context) error {
	if err := c.clickhouse.Exec(ctx, seed); err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	var count uint64
	if err := c.clickhouse.QueryRow(ctx, `SELECT COUNT(*) FROM events;`).Scan(&count); err != nil {
		return fmt.Errorf("SELECT events: %w", err)
	}

	slog.Info("seeded clickhouse events table with sample data", "count", count)
	return nil
}
