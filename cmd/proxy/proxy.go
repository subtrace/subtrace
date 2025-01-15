// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package proxy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/martian/v3"
	"github.com/google/uuid"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/version"
	"subtrace.dev/config"
	"subtrace.dev/devtools"
	"subtrace.dev/event"
	"subtrace.dev/global"
	"subtrace.dev/logging"
	"subtrace.dev/tags"
	"subtrace.dev/tracer"
)

type Command struct {
	ffcli.Command
	flags struct {
		config   string
		listen   string
		remote   string
		devtools string
		log      *bool
	}

	global *global.Global
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "proxy"
	c.ShortUsage = "subtrace proxy [flags]"
	c.ShortHelp = "start a reverse proxy"

	c.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	c.FlagSet.StringVar(&c.flags.config, "config", "", "configuration file path")
	c.FlagSet.StringVar(&c.flags.listen, "listen", "", "local address to listen on")
	c.FlagSet.StringVar(&c.flags.remote, "remote", "", "remote address to forward requests to")
	c.FlagSet.StringVar(&c.flags.devtools, "devtools", "/subtrace", "path to serve the chrome devtools bundle on")
	c.flags.log = c.FlagSet.Bool("log", false, "if true, log trace events to stderr")
	c.FlagSet.BoolVar(&logging.Verbose, "v", false, "enable verbose logging")

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE_PROXY")}
	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()
	slog.Debug("starting subtrace proxy", "release", version.Release, slog.Group("commit", "hash", version.CommitHash, "time", version.CommitTime), "build", version.BuildTime)

	if c.flags.listen == "" && c.flags.remote == "" {
		fmt.Fprintf(os.Stderr, "error: missing -listen and -remote\n")
		return flag.ErrHelp
	} else if c.flags.listen == "" {
		fmt.Fprintf(os.Stderr, "error: missing -listen\n")
		return flag.ErrHelp
	} else if c.flags.remote == "" {
		fmt.Fprintf(os.Stderr, "error: missing -remote\n")
		return flag.ErrHelp
	}

	c.global = new(global.Global)

	if c.flags.log == nil {
		c.flags.log = new(bool)
		if os.Getenv("SUBTRACE_TOKEN") == "" {
			*c.flags.log = true
		} else {
			*c.flags.log = false
		}
	} else if *c.flags.log == false && os.Getenv("SUBTRACE_TOKEN") == "" {
		exists := false
		for _, arg := range os.Args {
			if strings.Contains(arg, "-log") {
				exists = true
				break
			}
			if arg == "--" {
				break
			}
		}

		if exists {
			slog.Warn("subtrace proxy was started with -log=false but SUBTRACE_TOKEN is empty")
		}
	}

	tracer.DefaultManager.SetLog(*c.flags.log)

	tracer.InitPublisher(ctx)
	go tracer.DefaultManager.StartBackgroundFlush(ctx)
	defer func() {
		if err := tracer.DefaultManager.Flush(); err != nil {
			slog.Error("failed to flush tracer event manager", "err", err)
		}
	}()

	c.global.Config = config.New()
	if c.flags.config != "" {
		if err := c.global.Config.Load(c.flags.config); err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	}

	if c.flags.devtools != "" && !strings.HasPrefix(c.flags.devtools, "/") {
		c.flags.devtools = "/" + c.flags.devtools
	}
	c.global.Devtools = devtools.NewServer(c.flags.devtools)

	c.global.EventTemplate = event.New()
	for k, v := range c.global.Config.Tags {
		c.global.EventTemplate.Set(k, v)
	}
	go tags.SetLocalTagsAsync(c.global.EventTemplate)

	switch err := c.start(ctx); {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return nil
	default:
		slog.Error(err.Error())
		os.Exit(1)
		return nil
	}
}

func (c *Command) start(ctx context.Context) error {
	lis, err := net.Listen("tcp", c.flags.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	p := martian.NewProxy()
	p.SetRequestModifier(c)
	p.SetRoundTripper(c)
	p.SetDial(func(network string, addr string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, addr)
	})

	slog.Info("listening for new connections", "addr", c.flags.listen)
	if err := p.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func (c *Command) getRemotePrefix() string {
	if strings.HasPrefix(c.flags.remote, "http://") || strings.HasPrefix(c.flags.remote, "https://") {
		return c.flags.remote
	}
	return "http://" + c.flags.remote
}

func (c *Command) getRemoteHost() string {
	u, err := url.Parse(c.getRemotePrefix())
	if err != nil {
		return ""
	}
	return u.Host
}

func (c *Command) ModifyRequest(req *http.Request) error {
	if c.global.Devtools.HijackPath != "" && req.URL.Path == c.global.Devtools.HijackPath {
		conn, brw, err := martian.NewContext(req).Session().Hijack()
		if err != nil {
			return fmt.Errorf("subtrace: failed to hijack devtools endpoint: %w", err)
		}

		c.global.Devtools.HandleHijack(req, conn, brw)
		return nil
	}

	suffix := req.URL.EscapedPath()
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}

	u, err := url.Parse(c.getRemotePrefix() + suffix)
	if err != nil {
		return fmt.Errorf("parse remote addr: %w", err)
	}

	req.URL = u
	req.Host = c.getRemoteHost()
	req.Header.Del("accept-encoding")
	return nil
}

func (c *Command) RoundTrip(req *http.Request) (*http.Response, error) {
	eventID := uuid.New()

	parser := tracer.NewParser(c.global, eventID)
	parser.UseRequest(req)

	tr := &http.Transport{
		DisableCompression: true,
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if c.global.Config != nil {
		begin := time.Now()
		rule, found := c.global.Config.FindMatchingRule(req, resp)
		slog.Debug("ran config rules on request", "eventID", eventID, "took", time.Since(begin).Round(time.Microsecond))

		if found && rule.Then == "exclude" {
			return resp, nil
		}
	}

	parser.UseResponse(resp)
	go func() {
		if err := parser.Finish(); err != nil {
			slog.Error("failed to finish HAR entry insert", "eventID", eventID, "err", err)
		}
	}()

	return resp, nil
}
