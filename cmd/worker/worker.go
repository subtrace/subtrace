// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package worker

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/worker/clickhouse"
	"subtrace.dev/journal"
	"subtrace.dev/logging"
	"subtrace.dev/rpc"
)

type Command struct {
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

	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()
	slog.Info("starting worker node")

	if err := c.initClickhouse(ctx); err != nil {
		return fmt.Errorf("init clickhouse: %w", err)
	}
	defer c.clickhouse.Close()

	if err := c.loop(ctx); err != nil {
		return fmt.Errorf("upload handler loop: %w", err)
	}

	return nil
}

func (c *Command) initClickhouse(ctx context.Context) error {
	client, err := clickhouse.New(ctx)
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
	return nil
}

func (c *Command) loop(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	first := make(chan struct{}, 1)
	first <- struct{}{}

	var after time.Time
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-first:
		}

		now := time.Now().UTC()
		var items []*journal.ListUploads_Item
		for wait := time.Second; ; wait *= 2 {
			var resp journal.ListUploads_Response
			_, err := rpc.Call(ctx, &resp, "/api/ListUploads", &journal.ListUploads_Request{FinishAfterTime: after.UnixNano()})
			if err == nil {
				items = resp.Items
				break
			}
			if wait > 30*time.Second {
				return fmt.Errorf("list uploads: %w", err)
			}
			slog.Error("ListUploads failed, retrying after exponential backoff", "err", err, "wait", wait)
			time.Sleep(wait) // TODO: jitter
		}

		for _, item := range items {
			journalID, err := uuid.Parse(item.JournalId)
			if err != nil {
				return fmt.Errorf("invalid journal ID in ListUploads response: parse: %w", err)
			}
			if err := c.handleJournal(ctx, journalID, item.Url); err != nil {
				slog.Error("error processing journal", "journalID", journalID, "err", err)
				continue
			}
		}
		after = now
	}
}

func (c *Command) handleJournal(ctx context.Context, journalID uuid.UUID, url string) error {
	path, err := c.downloadJournal(ctx, journalID, url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(path)

	start := time.Now()
	slog.Debug("processing journal", "journalID", journalID, "path", path)

	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	d := newDecoder(bufio.NewReader(f), c.clickhouse)
	if err := d.decode(ctx); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	slog.Info("processed journal", "journalID", journalID, "fileSize", stat.Size(), "connCount", len(d.conn), "httpCount", len(d.http), "took", time.Since(start).Round(time.Microsecond))
	return nil
}

func (c *Command) downloadJournal(ctx context.Context, journalID uuid.UUID, url string) (_ string, finalErr error) {
	start := time.Now()
	slog.Debug("downloading journal", "journalID", journalID)

	dir := filepath.Join(os.TempDir(), "subtrace", "worker", "journals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.CreateTemp(dir, fmt.Sprintf("%s_%s_*.subtjrnl", time.Now().UTC().Format("20060102150405"), journalID))
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if finalErr != nil {
			os.Remove(f.Name())
		}
	}()
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	defer resp.Body.Close()

	if n, err := io.Copy(w, resp.Body); err != nil {
		return "", fmt.Errorf("copy: %w (copied %d bytes)", err, n)
	}

	slog.Debug("downloaded journal", "journalID", journalID, "took", time.Since(start).Round(time.Microsecond))
	return f.Name(), nil
}
