// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package socket

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/tls"
	"subtrace.dev/cmd/run/tracer"
	"subtrace.dev/event"
)

var hostname string

func init() {
	hostname, _ = os.Hostname()
}

type proxy struct {
	process  *net.TCPConn
	external *net.TCPConn

	begin         time.Time
	isOutgoing    bool
	tlsServerName *string
}

func (p *proxy) Close() error {
	errs := make(chan error, 2)

	go func() {
		if p.process == nil {
			errs <- nil
			return
		}
		switch err := p.process.Close(); {
		case err == nil:
		case errors.Is(err, net.ErrClosed):
		default:
			errs <- fmt.Errorf("close process side: %w", err)
			return
		}
		errs <- nil
	}()

	go func() {
		if p.external == nil {
			errs <- nil
			return
		}
		switch err := p.external.Close(); {
		case err == nil:
		case errors.Is(err, net.ErrClosed):
		default:
			errs <- fmt.Errorf("close external side: %w", err)
			return
		}
		errs <- nil
	}()

	return errors.Join(<-errs, <-errs)
}

func (p *proxy) LogValue() slog.Value {
	process := slog.String("process", "<nil>")
	if p.process != nil {
		process = slog.Group("process", "local", p.process.LocalAddr(), "remote", p.process.RemoteAddr())
	}

	external := slog.String("external", "<nil>")
	if p.external != nil {
		external = slog.Group("external", "local", p.external.LocalAddr(), "remote", p.external.RemoteAddr())
	}

	return slog.GroupValue(slog.Bool("outgoing", p.isOutgoing), process, external)
}

func (p *proxy) start() {
	if p.process == nil || p.external == nil {
		slog.Error("TCP proxy missing connection", "proxy", p)
		return
	}

	// TODO: should we match the tracee's TCP options on the external side?

	// There's no need for Nagle's algorithm on the process side since it's a
	// loopback connection.
	if err := p.process.SetNoDelay(true); err != nil {
		slog.Debug("failed to set TCP_NODELAY on process side", "proxy", p, "err", err) // not fatal
	}

	slog.Debug("starting tcp proxy", "proxy", p)
	defer func() {
		if err := p.Close(); err != nil {
			slog.Debug("failed close tcp proxy", "proxy", p, "err", err) // not fatal
		}
	}()

	cli, srv := newBufConn(p.process), newBufConn(p.external)
	if !p.isOutgoing {
		cli, srv = srv, cli
	}

	if err := p.proxyOptimistic(cli, srv); err != nil {
		slog.Error("failed to run tcp proxy", "proxy", p, "err", err)
	}
}

// proxyOptimistic peeks at buffered bytes available on the client side to
// guess the protocol. If a known protocol is found, it passes control to that
// protocol's handler. Otherwise, it falls back to the raw handler.
func (p *proxy) proxyOptimistic(cli, srv *bufConn) error {
	errs := make(chan error, 2)

	// While we need to sample client's TCP bytes to see if it's HTTP and/or TLS
	// or something else, we can't synchronously wait for the client's first
	// bytes. Doing so would break protocols that expect the server side to
	// talk first. HTTP and TLS don't behave this way, but since those are not
	// the only protocols we'll be proxying (we intercept all TCP connections at
	// the socket level), the sampling strategy needs to be robust.
	var race atomic.Bool

	go func() {
		if _, err := srv.peekSample(); err != nil {
			if race.Load() {
				errs <- fmt.Errorf("server: peek sample: %w", err)
			} else {
				errs <- nil
			}
			return
		}
		if winner := race.CompareAndSwap(false, true); !winner {
			errs <- nil
			return
		}

		// The second goroutine may still be waiting for the client's first bytes,
		// but it's safe for us to start the fallback proxy here because bufConn's
		// Read and Peek are goroutine-safe. When the second goroutine's peek
		// returns, it will observe that it has lost the race, thereby releasing
		// the client bufConn lock and exiting.
		errs <- p.proxyFallback(cli, srv)
	}()

	go func() {
		sample, err := cli.peekSample()
		if err != nil {
			if race.Load() {
				errs <- fmt.Errorf("client: peek sample: %w", err)
			} else {
				errs <- nil
			}
			return
		}
		if winner := race.CompareAndSwap(false, true); !winner {
			// Peeking the server's bytes finished before we could peek the client's
			// bytes. This is neither HTTP nor TLS. Since we lost the race, the other
			// goroutine must have already started the fallback proxy, so there's
			// nothing more to do.
			errs <- nil
			return
		}
		switch proto := guessProtocol(sample); proto {
		case "tls":
			errs <- p.proxyTLS(cli, srv)
		case "http/1":
			errs <- p.proxyHTTP(cli, srv)
		case "http/2":
			errs <- p.proxyHTTP(cli, srv)
		default:
			errs <- p.proxyFallback(cli, srv)
		}
	}()

	return errors.Join(<-errs, <-errs)
}

func (p *proxy) proxyTLS(cli, srv *bufConn) error {
	if !p.isOutgoing {
		// We can't intercept incoming TLS requests (yet). Doing so would require
		// some kind of cooperation from the tracee because the location of the CA
		// certificate and private key are application-specific.
		return p.proxyFallback(cli, srv)
	}

	if p.tlsServerName != nil {
		// TLS server name already exists? Could this be TLS within TLS? Bail out.
		return p.proxyFallback(cli, srv)
	}

	tcli, tsrv, serverName, err := tls.Handshake(cli, srv)
	if err != nil {
		// If the ephemeral MITM certificate we generated is not recognized, most
		// clients will close the connection during TLS handshake. This probably
		// means: (a) the application is using an unknown CA root store location,
		// (b) it's using certificate pinning, (c) it's an mTLS connection, or (d)
		// something else.
		//
		// TODO: this might be more common than we think, so we should allow the
		// user to disable TLS interception with a CLI flag.
		return fmt.Errorf("proxy tls handshake: %w", err)
	}

	p.tlsServerName = &serverName
	if err := p.proxyOptimistic(newBufConn(tcli), newBufConn(tsrv)); err != nil {
		return fmt.Errorf("proxy tls: %w", err)
	}
	return nil
}

// proxyHTTP proxies an HTTP connection between the client and server. If there
// are no buffered bytes available on the client side, it falls back to the raw
// handler.
func (p *proxy) proxyHTTP(cli, srv *bufConn) error {
	if cli.Buffered() == 0 {
		return p.proxyFallback(cli, srv)
	}

	sample, err := cli.peekSample()
	if err != nil {
		return p.proxyFallback(cli, srv)
	}

	protocol := guessProtocol(sample)
	switch protocol {
	case "http/1":
		return p.proxyHTTP1(cli, srv)
	case "http/2":
		// TODO: reintroduce HTTP/2 support
		return p.proxyFallback(cli, srv)
	default:
		return p.proxyFallback(cli, srv)
	}
}

func (p *proxy) proxyHTTP1(cli, srv *bufConn) error {
	errs := make(chan error, 3)

	cr, cw := io.Pipe()
	sr, sw := io.Pipe()

	go func() {
		defer func() { go io.Copy(io.Discard, cr) }()
		defer func() { go io.Copy(io.Discard, sr) }()

		cr, sr := bufio.NewReader(cr), bufio.NewReader(sr)
		for {
			req, err := http.ReadRequest(cr)
			switch {
			case err == nil:
			case errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed):
				errs <- nil
				return
			default:
				errs <- fmt.Errorf("tracer: read request: %w", err)
				return
			}

			if req.Body != nil {
				go func() {
					io.Copy(io.Discard, req.Body)
					req.Body.Close()
				}()
			}

			begin := time.Now()

			ev := event.New()

			ev.Set("pid", fmt.Sprintf("%d", os.Getpid()))

			if p.tlsServerName != nil && *p.tlsServerName != "" {
				ev.Set("tls_server_name", *p.tlsServerName)
			}

			ev.Set("http_version", req.Proto)
			ev.Set("http_is_outgoing", fmt.Sprintf("%v", p.isOutgoing))
			ev.Set("http_req_method", req.Method)
			ev.Set("http_req_path", req.URL.Path)
			if p.isOutgoing {
				ev.Set("http_client_addr", p.process.RemoteAddr().String())
				ev.Set("http_server_addr", p.external.RemoteAddr().String())
			} else {
				ev.Set("http_client_addr", p.external.RemoteAddr().String())
				ev.Set("http_server_addr", p.process.RemoteAddr().String())
			}

			resp, err := http.ReadResponse(sr, req)
			switch {
			case err == nil:
			case errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed):
				errs <- nil
				return
			default:
				errs <- fmt.Errorf("tracer: read response: %w", err)
				return
			}

			if resp.Body != nil {
				// Unlike the io.Copy(io.Discard, req.Body), we discard the response
				// body without a new goroutine so that the time.Now() call below
				// captures the HTTP request duration accurately.
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}

			ev.Set("http_duration", fmt.Sprintf("%d", time.Since(begin).Nanoseconds()))
			ev.Set("http_resp_status_code", fmt.Sprintf("http_resp_status_code=%d", resp.StatusCode))

			tags := ev.String()
			if val := os.Getenv("SUBTRACE_TAGS"); val != "" {
				tags += " " + val
			}
			if val := req.Header.Get("x-subtrace-tags"); val != "" {
				tags += " " + val
			}
			if val := resp.Header.Get("x-subtrace-tags"); val != "" {
				tags += " " + val
			}
			fmt.Printf("%s\n", tags)
			tracer.DefaultManager.Insert(tags)
		}
	}()

	go func() {
		defer srv.CloseWrite()
		defer cli.CloseRead()
		defer cw.Close()
		if err := p.copyRawSingle("client->server", "http/1", srv, io.TeeReader(cli, cw)); err != nil {
			errs <- fmt.Errorf("copy raw: client->server: %w", err)
			return
		}
		errs <- nil
	}()

	go func() {
		defer cli.CloseWrite()
		defer srv.CloseRead()
		defer sw.Close()
		if err := p.copyRawSingle("server->client", "http/1", cli, io.TeeReader(srv, sw)); err != nil {
			errs <- fmt.Errorf("copy raw: server->client: %w", err)
			return
		}
		errs <- nil
	}()

	if err := errors.Join(<-errs, <-errs, <-errs); err != nil {
		return fmt.Errorf("proxy http: %w", err)
	}
	return nil
}

func (p *proxy) proxyFallback(cli, srv *bufConn) error {
	errs := make(chan error, 2)

	go func() {
		defer srv.CloseWrite()
		defer cli.CloseRead()
		if err := p.copyRawSingle("client->server", "unknown", srv, cli); err != nil {
			errs <- fmt.Errorf("copy client->server: %w", err)
			return
		}
		errs <- nil
	}()

	go func() {
		defer cli.CloseWrite()
		defer srv.CloseRead()
		if err := p.copyRawSingle("server->client", "unknown", cli, srv); err != nil {
			errs <- fmt.Errorf("copy server->client: %w", err)
			return
		}
		errs <- nil
	}()

	if err := errors.Join(<-errs, <-errs); err != nil {
		return fmt.Errorf("raw fallback proxy: %w", err)
	}
	return nil
}

func (p *proxy) copyRawSingle(dir, proto string, w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, r)
	dur := time.Since(p.begin).Nanoseconds() / 1000
	switch {
	case err == nil:
	case errors.Is(err, net.ErrClosed):
	case errors.Is(err, unix.ECONNRESET):
	case errors.Is(err, unix.EPIPE):
	default:
		slog.Debug(fmt.Sprintf("copied bytes %s", dir), "proxy", p, "proto", proto, "bytes", n, "duration", dur, "err", err)
		return err
	}
	slog.Debug(fmt.Sprintf("copied bytes %s", dir), "proxy", p, "proto", proto, "bytes", n, "duration", dur)
	return nil
}

// bufConn is a net.Conn wrapper that supports peeking on the read side.
type bufConn struct {
	mu sync.Mutex
	r  *bufio.Reader
	net.Conn
}

func newBufConn(c net.Conn) *bufConn {
	return &bufConn{r: bufio.NewReader(c), Conn: c}
}

func (c *bufConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.r.Read(b)
}

func (c *bufConn) Buffered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.r.Buffered()
}

// peekSample waits for the first byte to become available in the read buffer
// and returns the buffered bytes without consuming them.
func (c *bufConn) peekSample() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch _, err := c.r.Peek(1); {
	case err == nil:
	case errors.Is(err, io.EOF):
		return nil, nil
	case errors.Is(err, net.ErrClosed):
		return nil, nil
	default:
		return nil, err
	}

	n := c.r.Buffered()
	b, err := c.r.Peek(n)
	if err != nil {
		panic(fmt.Errorf("impossible: peeking %d > 0 buffered bytes failed: %w", n, err))
	}
	return b, nil
}

// CloseWrite half-closes the write side of the connection. If the underlying
// net.Conn is not half-closeable (i.e. not a *net.TCPConn), this is a no-op.
func (c *bufConn) CloseWrite() error {
	if c, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return nil
}

// CloseRead half-closes the read side of the connection. If the underlying
// net.Conn is not half-closeable (i.e. not a *net.TCPConn), this is a no-op.
func (c *bufConn) CloseRead() error {
	if c, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return c.CloseRead()
	}
	return nil
}

func guessProtocol(sample []byte) string {
	markers := []struct {
		name  string
		skip  int
		bytes []byte
	}{
		{"http/1", 0, []byte("GET ")},
		{"http/1", 0, []byte("POST ")},
		{"http/1", 0, []byte("HEAD ")},
		{"http/1", 0, []byte("PUT ")},
		{"http/1", 0, []byte("DELETE ")},

		{"http/2", 0, []byte("PRI ")},

		{"tls", 0, []byte{0x16, 0x03, 0x01}},
		{"tls", 0, []byte{0x16, 0x03, 0x03}},
	}

	// More specific markers should be checked first.
	sort.Slice(markers, func(i, j int) bool {
		il := markers[i].skip + len(markers[i].bytes)
		jl := markers[j].skip + len(markers[j].bytes)
		if il == jl {
			return i < j
		}
		return il > jl
	})

	for _, m := range markers {
		if len(sample) <= m.skip {
			continue
		}
		b := sample[m.skip:]
		if bytes.HasPrefix(b, m.bytes) {
			return m.name
		}
	}

	return "unknown"
}
