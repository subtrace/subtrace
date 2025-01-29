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
	"strconv"
	"strings"

	"github.com/google/martian/v3"
	"github.com/google/uuid"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/version"
	"subtrace.dev/config"
	"subtrace.dev/devtools"
	"subtrace.dev/global"
	"subtrace.dev/logging"
	"subtrace.dev/tracer"
)

type Command struct {
	ffcli.Command
	flags struct {
		config   string
		devtools string
		log      *bool
	}

	from int
	to   int

	global *global.Global
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "proxy"
	c.ShortUsage = "subtrace proxy [flags] FROM:TO"
	c.ShortHelp = "proxy all requests from <FROM> port to <TO> port"

	c.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	c.FlagSet.StringVar(&c.flags.config, "config", "", "configuration file path")
	c.FlagSet.StringVar(&c.flags.devtools, "devtools", "/subtrace", "path to serve the chrome devtools bundle on")
	c.flags.log = c.FlagSet.Bool("log", false, "if true, log trace events to stderr")
	c.FlagSet.BoolVar(&logging.Verbose, "v", false, "enable verbose logging")
	c.UsageFunc = func(fc *ffcli.Command) string {
		return ffcli.DefaultUsageFunc(fc) + ExtraHelp()
	}

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE_PROXY")}
	c.Exec = c.entrypoint
	return &c.Command
}

func ExtraHelp() string {
	return strings.Join([]string{
		"",
		"EXAMPLE",
		"  subtrace proxy 9000:8000",
		"",
		"MORE",
		"  https://docs.subtrace.dev",
		"  https://subtrace.dev/discord",
		"",
	}, "\n")
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()
	slog.Debug("starting subtrace proxy", "release", version.Release, slog.Group("commit", "hash", version.CommitHash, "time", version.CommitTime), "build", version.BuildTime)

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "error: missing FROM:TO port numbers\n")
		return flag.ErrHelp
	}

	from, to, ok := strings.Cut(args[0], ":")
	if !ok {
		fmt.Fprintf(os.Stderr, "error: invalid FROM:TO port format\n")
		return flag.ErrHelp
	}

	var err error
	if c.from, err = strconv.Atoi(from); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid FROM port: parse as number: %v\n", err)
		return flag.ErrHelp
	}
	if c.to, err = strconv.Atoi(to); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid FROM port: parse as number: %v\n", err)
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
	addr := fmt.Sprintf(":%d", c.from)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	p := martian.NewProxy()
	p.SetRequestModifier(c)
	p.SetRoundTripper(c)
	p.SetDial(func(network string, addr string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, addr)
	})

	slog.Debug("listening for new connections", "addr", addr)
	if err := p.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
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
	if req.URL.RawQuery != "" {
		suffix += "?" + req.URL.RawQuery
	}

	u, err := url.Parse(fmt.Sprintf("http://0.0.0.0:%d", c.to) + suffix)
	if err != nil {
		return fmt.Errorf("parse remote addr: %w", err)
	}

	req.URL = u
	req.Header.Del("accept-encoding")
	return nil
}

func (c *Command) RoundTrip(req *http.Request) (*http.Response, error) {
	event := c.global.Config.GetEventTemplate()
	event.Set("event_id", uuid.New().String())

	parser := tracer.NewParser(c.global, event)
	parser.UseRequest(req)

	tr := &http.Transport{
		DisableCompression: true,
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	parser.UseResponse(resp)
	go func() {
		if err := parser.Finish(); err != nil {
			slog.Error("failed to finish HAR entry insert", "eventID", event.Get("event_id"), "err", err)
		}
	}()
	return resp, nil
}
