// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/proxy"
	"subtrace.dev/cmd/version"
	"subtrace.dev/cmd/worker"
)

var subcommands = []*ffcli.Command{
	proxy.NewCommand(),
	worker.NewCommand(),
	version.NewCommand(),
}
