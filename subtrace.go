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
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/proxy"
	"subtrace.dev/cmd/run"
	"subtrace.dev/cmd/validate"
	"subtrace.dev/cmd/version"
	"subtrace.dev/cmd/worker"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := new(ffcli.Command)
	c.Name = filepath.Base(os.Args[0])
	c.ShortUsage = "subtrace <command>"

	c.Subcommands = append(c.Subcommands, run.NewCommand())
	c.Subcommands = append(c.Subcommands, proxy.NewCommand())
	c.Subcommands = append(c.Subcommands, worker.NewCommand())
	c.Subcommands = append(c.Subcommands, version.NewCommand())
	c.Subcommands = append(c.Subcommands, validate.NewCommand())

	c.FlagSet = flag.NewFlagSet("subtrace", flag.ContinueOnError)
	c.FlagSet.SetOutput(os.Stdout)
	c.Exec = func(ctx context.Context, args []string) error {
		text := c.UsageFunc(c)
		if len(args) == 0 {
			text += run.ExtraHelp()
		}
		fmt.Fprintf(os.Stdout, "%s\n", text)

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

	if err := c.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "subtrace: error: %v\n", err)
		os.Exit(1)
	}
}
