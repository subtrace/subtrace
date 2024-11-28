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
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/dop251/goja"
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
		config string
		listen string
		remote string
		log    *bool
	}

	runtime *goja.Runtime
	rules   []rule

	ffcli.Command
}

func NewCommand() *ffcli.Command {
	c := new(Command)

	c.Name = "proxy"
	c.ShortUsage = "subtrace proxy [flags]"
	c.ShortHelp = "start a worker node"

	c.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	c.FlagSet.StringVar(&c.flags.config, "config", "", "configuration file path")
	c.FlagSet.StringVar(&c.flags.listen, "listen", "", "local IP:PORT to listen on")
	c.FlagSet.StringVar(&c.flags.remote, "remote", "", "remote address to forward requests to")
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

	if _, err := netip.ParseAddrPort(c.flags.listen); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to parse -listen address: %v\n", err)
		return flag.ErrHelp
	}

	if _, err := netip.ParseAddrPort(c.flags.remote); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to parse -remote address: %v\n", err)
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

	if c.flags.config != "" {
		b, err := os.ReadFile(c.flags.config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read config file: %v", err)
			os.Exit(1)
			return nil
		}

		if err := c.parseConfig(string(b)); err != nil {
			fmt.Fprintf(os.Stderr, "parse config: %v", err)
			os.Exit(1)
			return nil
		}
	}

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

func newStringMatcher(val goja.Value) (func(string) bool, error) {
	switch val := val.(type) {
	case goja.String:
		return func(x string) bool {
			ok, err := filepath.Match(val.String(), x)
			return err == nil && ok
		}, nil
	case *goja.Object:
		switch val.ClassName() {
		case "RegExp":
			re, err := regexp.Compile(val.Get("source").String())
			if err != nil {
				return nil, fmt.Errorf("compile regexp: %w", err)
			}
			return re.MatchString, nil
		}
	}
	return nil, fmt.Errorf("unknown type %T", val)
}

func (c *Command) parseConfig(config string) error {
	c.runtime = goja.New()

	var nextID int
	var mu sync.Mutex
	c.runtime.Set("trace", func(method goja.Value, path goja.Value, spec any) {
		id := nextID
		nextID++

		start := time.Now()
		defer func() {
			slog.Debug("parsing new trace config definition", "id", id, "method", method, "path", path, "spec", fmt.Sprintf("%p", spec), "rules", len(c.rules), "took", time.Since(start))
		}()

		// fmt.Printf("method(%T)=%+v  |  path(%T)=%+v\n", method, method, path, path)
		isMethodMatch, err := newStringMatcher(method)
		if err != nil {
			slog.Error("failed to build method matcher", "id", id, "err", err)
			return
		}
		isPathMatch, err := newStringMatcher(path)
		if err != nil {
			slog.Error("failed to build path matcher", "id", id, "err", err)
			return
		}

		mu.Lock()
		defer mu.Unlock()
		if fn, ok := spec.(func(goja.FunctionCall) goja.Value); ok {
			c.rules = append(c.rules, rule{
				match: func(req *http.Request, resp *http.Response) bool {
					return isMethodMatch(req.Method) && isPathMatch(req.URL.Path)
				},

				apply: func(req *http.Request, resp *http.Response) *action {
					ret := fn(goja.FunctionCall{})
					val := ret.Export()
					// fmt.Printf("applied %d: returned (%T=%T): %+v = %+v\n", id, ret, val, ret, val)

					switch val := val.(type) {
					case bool:
						return &action{skip: !val}
					case map[string]any:
						if x, ok := val["sample"]; ok {
							if p, ok := x.(float64); ok {
								if rand.Float64() >= p {
									return &action{skip: true}
								}
							}
						}
					}
					return &action{}
				},
			})
		}
	})

	start := time.Now()
	slog.Debug("running config builder script")
	if _, err := c.runtime.RunString(config); err != nil {
		panic(err)
	}

	slog.Info("finished parsing config script", "rules", len(c.rules), "took", time.Since(start).Round(time.Microsecond))
	return nil
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

func (c *Command) ModifyRequest(req *http.Request) error {
	req.URL.Host = c.flags.remote
	return nil
}

func (c *Command) RoundTrip(req *http.Request) (*http.Response, error) {
	begin := time.Now().UTC()

	ev := event.NewFromTemplate(event.Base)
	eventID := ev.Get("event_id")

	timings := new(har.Timings)
	timings.Send = time.Since(begin).Milliseconds()

	tr := &http.Transport{
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	httpDuration := time.Since(begin)
	timings.Wait = time.Since(begin).Milliseconds()
	timings.Receive = time.Since(begin).Milliseconds()

	action := new(action)
	for _, rule := range c.rules {
		if action.skip {
			break
		}
		if rule.match(req, resp) {
			action = action.merge(rule.apply(req, resp))
		}
	}

	slog.Debug("applied rules", "eventID", eventID, "took", (time.Since(begin) - httpDuration).Round(time.Microsecond))
	if action.skip {
		return resp, nil
	}

	switch true {
	case true:
		hreq, err := har.NewRequest(req, true)
		if err != nil {
			if hreq, err = har.NewRequest(req, false); err != nil {
				slog.Debug("failed to parse as HTTP request as HAR request", "eventID", eventID, "err", err)
				break
			} else {
				// TODO: tell the user that parsing the body failed for whatever reason
			}
		}

		hresp, err := har.NewResponse(resp, true)
		if err != nil {
			if hresp, err = har.NewResponse(resp, false); err != nil {
				slog.Debug("failed to parse as HTTP response as HAR response", "eventID", eventID, "err", err)
				break
			}
		}

		entry := &har.Entry{
			ID:              eventID,
			StartedDateTime: begin.UTC(),
			Time:            httpDuration.Milliseconds(),
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

type rule struct {
	match func(*http.Request, *http.Response) bool
	apply func(*http.Request, *http.Response) *action
}

type action struct {
	skip              bool
	requestModifiers  []martian.RequestModifier
	responseModifiers []martian.ResponseModifier
}

func (this *action) merge(other *action) *action {
	return &action{
		skip:              this.skip || other.skip,
		requestModifiers:  append(this.requestModifiers, other.requestModifiers...),
		responseModifiers: append(this.responseModifiers, other.responseModifiers...),
	}
}
