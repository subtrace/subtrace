package tracer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
	"nhooyr.io/websocket"
	"subtrace.dev/pubsub"
	"subtrace.dev/rpc"
)

var DefaultPublisher = &publisher{ch: make(chan []byte, 4096)}

type publisher struct {
	ch       chan []byte
	inflight sync.WaitGroup
	queued   sync.WaitGroup
}

func (p *publisher) dialSingle(ctx context.Context) (*websocket.Conn, string, error) {
	req := &pubsub.JoinPublisher_Request{}

	var opts []rpc.Option
	if os.Getenv("SUBTRACE_TOKEN") == "" {
		linkID := os.Getenv("SUBTRACE_LINK_ID_OVERRIDE")
		if linkID != "" {
			req.LinkIdOverride = &linkID
		}

		opts = append(opts, rpc.WithoutToken())
	} else {
		opts = append(opts, rpc.WithToken())
	}

	var pub pubsub.JoinPublisher_Response
	if code, err := rpc.Call(ctx, &pub, "/api/JoinPublisher", req, opts...); err != nil {
		return nil, "", fmt.Errorf("call JoinPublisher: %w", err)
	} else if code != http.StatusOK || (pub.Error != nil && *pub.Error != "") {
		err := fmt.Errorf("JoinPublisher: %s", http.StatusText(code))
		if pub.Error != nil && *pub.Error != "" {
			err = fmt.Errorf("%w: %s", err, *pub.Error)
		}
		return nil, "", err
	}

	u, err := url.Parse(pub.WebsocketUrl)
	if err != nil {
		return nil, "", fmt.Errorf("parse url: %w", err)
	}

	slog.Debug("dialing publisher websocket", "namespaceID", u.Query().Get("namespaceID"), "expiry", u.Query().Get("expiry"))
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: http.DefaultClient,
		HTTPHeader: rpc.GetHeader(),
	})
	if err != nil {
		err := fmt.Errorf("websocket dial: %w", err)
		if resp != nil {
			err = fmt.Errorf("%w: %s", err, http.StatusText(resp.StatusCode))
			if resp.Body != nil {
				if b, err2 := io.ReadAll(resp.Body); err2 != nil && len(b) > 0 {
					err = fmt.Errorf("%w: %s", err, string(b))
				}
			}
		}
		return nil, "", err
	}
	conn.SetReadLimit(16 << 20)

	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()

	return conn, u.Query().Get("url"), nil
}

func (p *publisher) wait(ctx context.Context, dur time.Duration) bool {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *publisher) dial(ctx context.Context) (*websocket.Conn, string) {
	var backoff time.Duration
	for i := 0; ; i++ {
		conn, url, err := p.dialSingle(ctx)
		if err != nil {
			backoff += time.Second
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			if ok := p.wait(ctx, backoff); !ok {
				return nil, ""
			}
			slog.Debug("failed to dial websocket", "err", err, "attempt", i)
			continue
		} else {
			return conn, url
		}
	}
}

func box(title string, body ...string) {
	const (
		HH = "─"
		VV = "│"
		LT = "╭"
		RT = "╮"
		LB = "╰"
		RB = "╯"
	)

	width := 60
	for _, line := range body {
		if 2+len(line)+2 > width {
			width = 2 + len(line) + 2
		}
	}

	prefix, suffix := "", ""
	if term.IsTerminal(int(os.Stderr.Fd())) {
		prefix, suffix = "\033[0;34m", "\033[0m"
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, body...)
	lines = append(lines, "")

	b := new(bytes.Buffer)
	fmt.Fprintf(b, "\n")
	fmt.Fprintf(b, "%s%s%s %s %s%s%s\n", prefix, LT, strings.Repeat(HH, (width-(1+1+len(title)+1+1))/2), title, strings.Repeat(HH, (width-(1+1+len(title)+1+1))/2), RT, suffix)
	for _, line := range lines {
		lp, ls := "", ""
		if term.IsTerminal(int(os.Stderr.Fd())) && strings.Contains(line, "https://") {
			lp, ls = "\033[0;34m", "\033[0m"
		}
		fmt.Fprintf(b, "%s%s%s  %s"+fmt.Sprintf("%%-%ds", width-3-3)+"%s  %s%s%s\n", prefix, VV, suffix, lp, line, ls, prefix, VV, suffix)
	}
	fmt.Fprintf(b, "%s%s%s%s%s\n", prefix, LB, strings.Repeat(HH, (width-(1+1))), RB, suffix)
	fmt.Fprintf(b, "\n")

	// Write it out all at once so that there's no interference with logs from
	// the child process.
	os.Stderr.Write(b.Bytes())
}

func (p *publisher) showURL(val string) {
	if val == "" {
		return
	}

	box(
		"SUBTRACE",
		"Connected! Use this link to see your API requests:",
		"",
		"    "+val,
		"",
		"Thanks for using Subtrace, happy debugging!",
	)
}

func (p *publisher) queueWrite(b []byte) error {
	p.queued.Add(1)
	select {
	case DefaultPublisher.ch <- b:
		return nil
	default:
		p.queued.Done()
		return fmt.Errorf("publisher channel buffer full")
	}
}

func (p *publisher) Loop(ctx context.Context) {
	var conn *websocket.Conn
	defer func() {
		if conn != nil {
			conn.CloseNow()
		}
	}()

	if conn == nil {
		c, url := p.dial(ctx)
		if c == nil { // context deadline exceeded
			return
		} else {
			conn = c
			p.showURL(url)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case b := <-p.ch:
			for i := 0; ; i++ {
				if conn == nil {
					conn, _ = p.dial(ctx)
					if conn == nil { // context deadline exceeded
						return
					}
				}

				if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
					slog.Debug("failed to write to websocket", "err", err)
					conn.CloseNow()
					conn = nil
					if i > 0 {
						if !p.wait(ctx, time.Second) {
							return
						}
					}
				} else {
					p.queued.Done()
					break
				}
			}
		}
	}
}

func (p *publisher) Flush(timeout time.Duration) (flushed bool) {
	waitEmpty := func(timeout time.Duration, wg *sync.WaitGroup) bool {
		ch := make(chan struct{})
		go func() {
			defer close(ch)
			wg.Wait()
		}()

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-ch:
			return true
		case <-timer.C:
			return false
		}
	}

	start := time.Now()
	waitEmpty(timeout/2, &p.inflight)
	if left := timeout - time.Since(start); left > 0 {
		return waitEmpty(left, &p.queued)
	} else {
		// This can happen only if timeout is zero or if the first Go timer
		// triggered inaccurately. Both are weird outcomes but there's no way for
		// us to actually guarantee to the caller that we've flushed all data, so
		// returning false is the only thing that makes sense.
		return false
	}
}
