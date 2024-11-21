// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/google/martian/v3"
	"github.com/google/martian/v3/har"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"subtrace.dev/cmd/version"
	"subtrace.dev/event"
	"subtrace.dev/logging"
	"subtrace.dev/tags/cloudtags"
	"subtrace.dev/tags/gcptags"
	"subtrace.dev/tags/kubetags"
	"subtrace.dev/tracer"
)

type Command struct {
	flags struct {
		from string
		to   string
		log  *bool
	}

	ffcli.Command
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "proxy"
	c.ShortUsage = "subtrace proxy [flags]"
	c.ShortHelp = "start a worker node"

	c.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	c.FlagSet.StringVar(&c.flags.from, "from", "", "local address to listen on")
	c.FlagSet.StringVar(&c.flags.to, "to", "", "remote address to forward requests to")
	c.flags.log = c.FlagSet.Bool("log", false, "if true, log trace events to stderr")
	c.FlagSet.BoolVar(&logging.Verbose, "v", false, "enable verbose logging")

	c.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE_PROXY")}
	c.Exec = c.entrypoint
	return &c.Command
}

func (c *Command) entrypoint(ctx context.Context, args []string) error {
	logging.Init()
	slog.Debug("starting subtrace proxy", "release", version.Release, slog.Group("commit", "hash", version.CommitHash, "time", version.CommitTime), "build", version.BuildTime)

	if c.flags.from == "" && c.flags.to == "" {
		fmt.Fprintf(os.Stderr, "error: missing -from=ADDR and -to=ADDR\n")
		return flag.ErrHelp
	} else if c.flags.from == "" {
		fmt.Fprintf(os.Stderr, "error: missing -from=ADDR\n")
		return flag.ErrHelp
	} else if c.flags.to == "" {
		fmt.Fprintf(os.Stderr, "error: missing -to=ADDR\n")
		return flag.ErrHelp
	}

	if _, err := netip.ParseAddrPort(c.flags.from); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to parse -from address: %v\n", err)
		return flag.ErrHelp
	}

	if _, err := netip.ParseAddrPort(c.flags.to); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to parse -to address: %v\n", err)
		return flag.ErrHelp
	}

	if c.flags.log == nil {
		c.flags.log = new(bool)
		if os.Getenv("SUBTRACE_TOKEN") == "" {
			*c.flags.log = true
		} else {
			*c.flags.log = false
		}
	} else if *c.flags.log == false {
		if os.Getenv("SUBTRACE_TOKEN") == "" {
			slog.Warn("subtrace proxy was started with -log=false but SUBTRACE_TOKEN is empty")
		}
	}

	tracer.DefaultManager.SetLog(*c.flags.log)

	c.initEventBase()

	go tracer.DefaultManager.StartBackgroundFlush(ctx)
	defer func() {
		if err := tracer.DefaultManager.Flush(); err != nil {
			slog.Error("failed to flush tracer event manager", "err", err)
		}
	}()

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

func (c *Command) initEventBase() {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	event.Base.Set("hostname", hostname)

	cloud := cloudtags.CloudUnknown
	cloudBarrier := make(chan struct{})
	go func() {
		defer close(cloudBarrier)
		if cloud = cloudtags.GuessCloudDMI(); cloud == cloudtags.CloudUnknown {
			cloud = cloudtags.GuessCloudIMDS()
		}
	}()

	go func() {
		<-cloudBarrier
		switch cloud {
		case cloudtags.CloudGCP:
			c := gcptags.New()
			if project, err := c.Get("/computeMetadata/v1/project/project-id"); err == nil {
				event.Base.Set("gcp_project", project)
			}
		}
	}()

	if partial, ok := kubetags.FetchLocal(); ok {
		event.Base.CopyFrom(partial)
		go func() {
			<-cloudBarrier
			switch cloud {
			case cloudtags.CloudGCP:
				event.Base.CopyFrom(kubetags.FetchGKE())
			}
		}()
	}
}

func (c *Command) start(ctx context.Context) error {
	lis, err := net.Listen("tcp", c.flags.from)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	p := martian.NewProxy()
	p.SetRequestModifier(c)
	p.SetRoundTripper(c)
	p.SetDial(func(network string, addr string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, addr)
	})

	slog.Info("listening for new connections", "addr", c.flags.from)
	if err := p.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func (c *Command) ModifyRequest(req *http.Request) error {
	req.URL.Host = c.flags.to
	return nil
}

func (c *Command) RoundTrip(req *http.Request) (*http.Response, error) {
	begin := time.Now().UTC()

	timings := new(har.Timings)
	timings.Send = time.Since(begin).Milliseconds()

	req.RequestURI = ""
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return resp, err
	}

	duration := time.Since(begin)
	timings.Wait = time.Since(begin).Milliseconds()
	timings.Receive = time.Since(begin).Milliseconds()

	ev := event.NewFromTemplate(event.Base)
	eventID := ev.Get("event_id")

	switch true {
	case true:
		hreq, err := har.NewRequest(req, false)
		if err != nil {
			slog.Debug("failed to parse as HTTP request as HAR request", "eventID", eventID, "err", err)
			break
		}

		hresp, err := har.NewResponse(resp, false)
		if err != nil {
			slog.Debug("failed to parse as HTTP response as HAR response", "eventID", eventID, "err", err)
			break
		}

		entry := &har.Entry{
			ID:              eventID,
			StartedDateTime: begin.UTC(),
			Time:            duration.Milliseconds(),
			Request:         hreq,
			Response:        hresp,
			Timings:         timings,
		}

		b := new(bytes.Buffer)
		w := base64.NewEncoder(base64.RawStdEncoding, b)
		if err := json.NewEncoder(w).Encode(entry); err != nil {
			slog.Debug("failed to encode HAR JSON", "eventID", eventID, "err", err)
			break
		}
		if err := w.Close(); err != nil {
			slog.Debug("failed to close HAR base64 encoder", "eventID", eventID, "err", err)
			break
		}

		ev.Set("http_har_entry", b.String())
	}

	tracer.DefaultManager.Insert(ev.String())
	return resp, nil
}
