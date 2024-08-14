// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"subtrace.dev/config"
)

type Option func(*http.Request) error

func WithoutAuth() Option {
	return func(r *http.Request) error {
		r.Header.Del("authorization")
		return nil
	}
}

func Call[R any, PR ptr[R]](ctx context.Context, w proto.Message, path string, r *R, opts ...Option) (int, error) {
	b, err := protojson.Marshal(PR(r))
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, "POST", config.ControlURL()+path, bytes.NewBuffer(b))
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("authorization", fmt.Sprintf("Bearer %s", os.Getenv("SUBTRACE_TOKEN")))

	var errs []error
	for _, opt := range opts {
		if err := opt(req); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return 0, fmt.Errorf("apply options on request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	defer func() {
		slog.Debug("http request", slog.Group("req",
			"id", resp.Header.Get("x-subtrace-id"),
			"method", req.Method,
			"host", req.URL.Host,
			"path", req.URL.Path,
			"code", resp.StatusCode,
			"status", resp.Status,
			"took", time.Since(start).Round(time.Microsecond),
		))
	}()

	if resp.StatusCode >= 500 {
		return resp.StatusCode, fmt.Errorf("%s", resp.Status)
	}

	b, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // 64 MB limit
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if err := protojson.Unmarshal(b, w); err != nil {
		if errors.Is(err, io.EOF) {
			return resp.StatusCode, fmt.Errorf("%s", resp.Status)
		}
		return resp.StatusCode, fmt.Errorf("unmarshal: %s: %w", resp.Status, err)
	}
	if e, ok := w.(interface{ GetError() string }); ok {
		if err := e.GetError(); err != "" {
			return resp.StatusCode, fmt.Errorf("client error: %s: %s", resp.Status, err)
		}
	}
	return resp.StatusCode, nil
}

type ptr[T any] interface {
	*T
	proto.Message
}
