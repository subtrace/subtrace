// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := new(ffcli.Command)
	c.Name = filepath.Base(os.Args[0])
	c.ShortUsage = "subtrace <command>"
	c.Subcommands = subcommands

	c.FlagSet = flag.NewFlagSet("subtrace", flag.ContinueOnError)
	c.FlagSet.SetOutput(os.Stdout)
	c.Exec = func(ctx context.Context, args []string) error {
		fmt.Fprintf(os.Stdout, "%s\n", c.UsageFunc(c))

		// Using os.Args and not args[0] because `subtrace -- curl` calls Exec with
		// args={"curl"}, not args={"--", "curl"}. The real error is the lack of
		// the run subcommand (explicit is better than implicit).
		if len(os.Args) >= 2 {
			return fmt.Errorf("unknown command %q", os.Args[1])
		}
		return nil
	}

	switch err := c.Parse(os.Args[1:]); {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		return
	case strings.Contains(err.Error(), "flag provided but not defined"):
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "subtrace: error: %v\n", err)
		os.Exit(1)
	}

	if runtime.GOOS == "darwin" && len(os.Args) >= 2 && os.Args[1] == "run" {
		fmt.Fprintf(os.Stderr, "The `subtrace run` command is currently not supported on macOS.\n")
		fmt.Fprintf(os.Stderr, "You can use the `subtrace proxy` command instead.\n")
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "See https://docs.subtrace.dev/macos for more details.\n")
		return
	}

	if err := c.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "subtrace: error: %v\n", err)
		os.Exit(1)
	}
}
