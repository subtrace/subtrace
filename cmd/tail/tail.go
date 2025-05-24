// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package tail

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/martian/v3/har"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
	"subtrace.dev/logging"
	"subtrace.dev/pubsub"
	"subtrace.dev/rpc"
)

type filters []string

func (f *filters) String() string {
	var ret []string
	for _, s := range *f {
		ret = append(ret, fmt.Sprintf("%q", s))
	}
	return strings.Join(ret, " ")
}

func (f *filters) Set(s string) error {
	*f = append(*f, s)
	return nil
}

type Tail struct {
	ffcli.Command
	flags struct {
		filters filters
		format  string
	}
}

func NewCommand() *ffcli.Command {
	t := new(Tail)

	t.Name = "tail"
	t.ShortUsage = "subtrace tail [flags]"
	t.ShortHelp = "tail network requests in realtime"

	t.FlagSet = flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	t.FlagSet.Var(&t.flags.filters, "filter", "list of filters (multiple okay)")
	t.FlagSet.StringVar(&t.flags.format, "format", "text", "either text (default) or json")
	t.FlagSet.BoolVar(&logging.Verbose, "v", false, "enable verbose logging")
	t.FlagSet.StringVar(&logging.Logfile, "logfile", "", "file for debug logs (stdout if unspecified)")

	t.Options = []ff.Option{ff.WithEnvVarPrefix("SUBTRACE_TAIL")}
	t.Exec = t.entrypoint
	return &t.Command
}

func (t *Tail) entrypoint(ctx context.Context, args []string) error {
	if err := logging.Init(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}

	if os.Getenv("SUBTRACE_TOKEN") == "" {
		fmt.Fprintf(os.Stderr, "subtrace: error: missing SUBTRACE_TOKEN")
		os.Exit(1)
		return nil
	}

	switch t.flags.format {
	case "text":
	case "json":
	default:
		return fmt.Errorf("unknown format %q", t.flags.format)
	}

	if err := t.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "subtrace: %v", err)
		os.Exit(1)
		return nil
	}
	return nil
}

func (t *Tail) run(ctx context.Context) error {
	slog.Debug("connecting as subcriber", "filters", t.flags.filters)

	conn, err := dialWebsocket(ctx)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close(websocket.StatusGoingAway, "")

	slog.Debug("established websocket connection")

	errs := make(chan error, 1)

	sub := t.newSubscriber(conn)
	go func() {
		if err := sub.readLoop(ctx); err != nil {
			errs <- fmt.Errorf("read loop: %w", err)
			return
		}
	}()

	if err := sub.initFilters(ctx); err != nil {
		return fmt.Errorf("init filters: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

type subscriber struct {
	tail *Tail
	conn *websocket.Conn

	revision atomic.Uint64
	ready    chan struct{}
	errs     chan error
}

func (t *Tail) newSubscriber(conn *websocket.Conn) *subscriber {
	return &subscriber{
		tail:  t,
		conn:  conn,
		ready: make(chan struct{}),
		errs:  make(chan error, 1),
	}
}

func (s *subscriber) initFilters(ctx context.Context) error {
	revision := s.revision.Add(1)
	if err := s.send(ctx, &pubsub.Message{
		Concrete: &pubsub.Message_ConcreteV1{
			ConcreteV1: &pubsub.Message_V1{
				Underlying: &pubsub.Message_V1_SetSubscriberConfig{
					SetSubscriberConfig: &pubsub.SetSubscriberConfig{
						Concrete: &pubsub.SetSubscriberConfig_ConcreteV1{
							ConcreteV1: &pubsub.SetSubscriberConfig_V1{
								Type: &pubsub.SetSubscriberConfig_V1_Call_{
									Call: &pubsub.SetSubscriberConfig_V1_Call{
										Revision: revision,
										Filters:  s.tail.flags.filters,
									},
								},
							},
						},
					},
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	slog.Debug("sent filter list for server-side filtering", "revision", revision, "filters", len(s.tail.flags.filters))

	select {
	case <-ctx.Done():
		return nil
	case <-s.ready:
		return nil
	case err := <-s.errs:
		return err
	}
}

func (s *subscriber) send(ctx context.Context, m *pubsub.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal proto: %w", err)
	}
	return s.conn.Write(ctx, websocket.MessageBinary, b)
}

func (s *subscriber) readLoop(ctx context.Context) error {
	for {
		typ, b, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if typ != websocket.MessageBinary {
			return fmt.Errorf("unexpected message type %q", typ.String())
		}

		x := new(pubsub.Message)
		if err := proto.Unmarshal(b, x); err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}

		if err := s.Message(x); err != nil {
			return fmt.Errorf("handle: %w", err)
		}
	}
}

func (s *subscriber) Event_V1(x *pubsub.Event_V1) error {
	select {
	case <-s.ready:
	default:
		slog.Debug("dropping event because filters are yet to be applied", "event", fmt.Sprintf("%p", x))
		return nil
	}

	var entry har.Entry
	if err := json.Unmarshal(x.HarEntryJson, &entry); err != nil {
		return fmt.Errorf("unmarshal HAR json: %w", err)
	}

	switch s.tail.flags.format {
	case "text":
		fmt.Printf("%8s    %d    %s %q\n", time.Duration(entry.Time)*time.Millisecond, entry.Response.Status, entry.Request.Method, entry.Request.URL)
	case "json":
		fmt.Printf("%s\n", x.HarEntryJson)
	}

	return nil
}

func (s *subscriber) Event(x *pubsub.Event) error {
	if x.GetConcrete() == nil {
		return fmt.Errorf("nil concrete")
	}

	var err error
	switch x := x.GetConcrete().(type) {
	case *pubsub.Event_ConcreteV1:
		err = s.Event_V1(x.ConcreteV1)
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		return fmt.Errorf("handle %T: %w", x.GetConcrete(), err)
	}
	return nil
}

func (s *subscriber) SetSubscriberConfig_V1(x *pubsub.SetSubscriberConfig_V1) error {
	result := x.GetResult()
	if result == nil {
		return fmt.Errorf("empty result")
	}

	if result.Revision < s.revision.Load() {
		return nil
	}
	if result.Error != nil {
		s.errs <- fmt.Errorf("SetSubscriberConfig: %s", result.GetError())
		return nil
	}

	slog.Debug("applied server-side filters")
	close(s.ready)
	return nil
}

func (s *subscriber) SetSubscriberConfig(x *pubsub.SetSubscriberConfig) error {
	if x.GetConcrete() == nil {
		return fmt.Errorf("nil concrete")
	}

	var err error
	switch x := x.GetConcrete().(type) {
	case *pubsub.SetSubscriberConfig_ConcreteV1:
		err = s.SetSubscriberConfig_V1(x.ConcreteV1)
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		return fmt.Errorf("handle %T: %w", x.GetConcrete(), err)
	}
	return nil
}

func (s *subscriber) handleMessageV1(x *pubsub.Message_V1) error {
	if x.GetUnderlying() == nil {
		return fmt.Errorf("nil underlying")
	}

	var err error
	switch x := x.GetUnderlying().(type) {
	case *pubsub.Message_V1_Event:
		err = s.Event(x.Event)
	case *pubsub.Message_V1_SetSubscriberConfig:
		err = s.SetSubscriberConfig(x.SetSubscriberConfig)
	case *pubsub.Message_V1_AnnounceStats:
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		return fmt.Errorf("handle %T: %w", x.GetUnderlying(), err)
	}
	return nil
}

func (s *subscriber) Message(x *pubsub.Message) error {
	if x.GetConcrete() == nil {
		return fmt.Errorf("nil concrete")
	}

	var err error
	switch x := x.GetConcrete().(type) {
	case *pubsub.Message_ConcreteV1:
		err = s.handleMessageV1(x.ConcreteV1)
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		return fmt.Errorf("handle %T: %w", x.GetConcrete(), err)
	}
	return nil
}

func dialWebsocket(ctx context.Context) (*websocket.Conn, error) {
	u, err := getWebsocketURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("get websocket url: %w", err)
	}

	slog.Debug("dialing subscriber websocket", "namespaceID", u.Query().Get("namespaceID"), "expiry", u.Query().Get("expiry"))
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: rpc.GetHeader()})
	if err != nil {
		err := fmt.Errorf("websocket dial: %w", err)
		if resp != nil {
			err = fmt.Errorf("%w: %s", err, http.StatusText(resp.StatusCode))
			if resp.Body != nil {
				defer resp.Body.Close()
				if b, err2 := io.ReadAll(resp.Body); err2 != nil && len(b) > 0 {
					err = fmt.Errorf("%w: %s", err, string(b))
				}
			}
		}
		return nil, err
	}
	conn.SetReadLimit(16 << 20)

	return conn, nil
}

func getWebsocketURL(ctx context.Context) (*url.URL, error) {
	var resp pubsub.JoinSubscriber_Response
	code, err := rpc.Call(ctx, &resp, "/api/JoinSubscriber", &pubsub.JoinSubscriber_Request{})
	if err != nil {
		return nil, fmt.Errorf("call: %w", err)
	}
	if code != http.StatusOK {
		err := fmt.Errorf("%s", http.StatusText(code))
		if resp.GetError() != "" {
			err = fmt.Errorf("%w: %s", err, resp.GetError())
		}
		return nil, err
	}
	u, err := url.Parse(resp.WebsocketUrl)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	return u, nil
}
