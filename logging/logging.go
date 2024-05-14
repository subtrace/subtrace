// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var Verbose bool

func Init() {
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
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
}
