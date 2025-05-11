// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package logging

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var Verbose bool
var Logfile string

func Init() error {
	_, path, _, _ := runtime.Caller(0)
	prefix := strings.TrimSuffix(path, "/logging/logging.go")

	level := &slog.LevelVar{}
	if Verbose {
		level.Set(slog.LevelDebug)
	} else {
		level.Set(slog.LevelInfo)
	}

	opts := &slog.HandlerOptions{
		AddSource: true,
		Level:     level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case "source":
				src := attr.Value.Any().(*slog.Source)
				src.File = strings.TrimPrefix(src.File, prefix+"/")
				src.File = strings.TrimPrefix(src.File, filepath.Dir(prefix)+"/")
				return slog.Attr{Key: "src", Value: attr.Value}
			case "msg":
				msg := attr.Value.Any().(string)
				if msg == "" {
					return slog.Attr{}
				}
			}
			return attr
		},
	}

	out := os.Stdout
	if Logfile != "" {
		f, err := os.OpenFile(Logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("create logfile: %w", err)
		}
		out = f
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, opts)))

	return nil
}
