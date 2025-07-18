// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package socket

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/martian/v3"
	"github.com/google/uuid"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"golang.org/x/sys/unix"
	"subtrace.dev/cmd/run/tls"
	"subtrace.dev/event"
	"subtrace.dev/global"
	"subtrace.dev/tracer"
)

type proxy struct {
	global *global.Global
	tmpl   *event.Event

	begin      time.Time
	socket     *Socket
	isOutgoing bool

	process  *net.TCPConn
	external *net.TCPConn

	tlsServerName atomic.Pointer[string]

	// skipCloseTCP denotes whether the underlying process and external TCPConn
	// should be closed. Both (*Socket).Close() and (*proxy).start() race to
	// change this from false to true with a CAS. Whoever loses the CAS will
	// close the two TCP connections.
	skipCloseTCP atomic.Bool
}

func newProxy(global *global.Global, tmpl *event.Event, isOutgoing bool) *proxy {
	return &proxy{
		global: global,
		tmpl:   tmpl,

		begin:      time.Now(),
		isOutgoing: isOutgoing,
	}
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

	socket := slog.String("sock", "<nil>")
	if p.socket != nil {
		// Do not log the whole socket object because that leads to an infinite
		// recursion due to (*Socket).LogValue() logging the proxy via the inode.
		socket = slog.Group("sock", "fd", p.socket.FD)
	}

	tlsServerName := slog.String("tlsServerName", "<nil>")
	if val := p.tlsServerName.Load(); val != nil {
		tlsServerName = slog.String("tlsServerName", *val)
	}

	return slog.GroupValue(
		slog.Bool("outgoing", p.isOutgoing),
		socket,
		tlsServerName,
		process,
		external,
	)
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

	if p.skipCloseTCP.CompareAndSwap(false, true) {
		// The target program has still not called the close(2) syscall on its file
		// descriptor. When it does, the (*Socket).Close() handler will close the
		// two underlying TCP connections. See the equivalent CAS in socket.go for
		// the process.Close() and external.Close() calls.
	} else {
		p.process.Close()
		p.external.Close()
	}
}

var isHTTP2Enabled = false
var isWebsocketEnabled = false
var websocketTimeLimit time.Duration = 110 * time.Second

func Init() error {
	for _, name := range []string{"SUBTRACE_HTTP2", "SUBTRACE_GRPC"} {
		switch strings.ToLower(os.Getenv(name)) {
		case "1", "y", "yes", "t", "true":
			isHTTP2Enabled = true
		}
	}

	switch strings.ToLower(os.Getenv("SUBTRACE_WEBSOCKET")) {
	case "1", "t", "true", "y", "yes":
		isWebsocketEnabled = true
		val := os.Getenv("SUBTRACE_WEBSOCKET_TIME_LIMIT")
		if val != "" {
			t, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("parse SUBTRACE_WEBSOCKET_TIME_LIMIT: %w", err)
			}
			websocketTimeLimit = time.Duration(t) * time.Second
		}
	}
	return nil
}

// proxyOptimistic peeks at buffered bytes available on the client side to
// guess the protocol. If a known protocol is found, it passes control to that
// protocol's handler. Otherwise, it falls back to the raw handler.
func (p *proxy) proxyOptimistic(cli, srv *bufConn) error {
	slog.Debug("starting proxyOptimistic", "proxy", p)

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

		if isHTTP2Enabled {
			// Give the client up to few hundred milliseconds to catch up so it has a
			// better chance of winning the race in situations where the first server
			// bytes arrive fractions of a millisecond before the first client bytes
			// (e.g. when a HTTP/2 server pushes a SETTINGS frame before the client
			// sends its first frame). Doing this greatly increases wire protocol
			// guess accuracy. While this admittedly adds upto 1ms latency in obscure
			// protocols where the server is required to talk first, the trade-off is
			// well worth it because Subtrace is not designed for those protocols.
			start := time.Now()
			for i := 0; ; i++ {
				if race.Load() {
					break
				}
				if i&15 == 15 {
					time.Sleep(20 * time.Microsecond)
				}
				if race.Load() {
					break
				}
				if time.Since(start) >= 1000*time.Microsecond {
					break
				}
				if race.Load() {
					break
				}
				runtime.Gosched()
				if race.Load() {
					break
				}
			}
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

		protocol := guessProtocol(sample)
		slog.Debug("guessed protocol", "proxy", p, "protocol", protocol)
		switch protocol {
		case "tls":
			if tls.Enabled {
				errs <- p.proxyTLS(cli, srv)
			} else {
				errs <- p.proxyFallback(cli, srv)
			}
		case "http/1":
			errs <- p.proxyHTTP1(cli, srv)
		case "http/2":
			errs <- p.proxyHTTP2(cli, srv)
		default:
			errs <- p.proxyFallback(cli, srv)
		}
	}()

	return errors.Join(<-errs, <-errs)
}

func (p *proxy) proxyTLS(cli, srv *bufConn) error {
	slog.Debug("starting proxyTLS", "proxy", p)

	if !p.isOutgoing {
		// We can't intercept incoming TLS requests (yet). Doing so would require
		// some kind of cooperation from the tracee because the location of the CA
		// certificate and private key are application-specific.
		return p.proxyFallback(cli, srv)
	}

	if p.tlsServerName.Load() != nil {
		// TLS server name already exists? Could this be TLS within TLS? Bail out.
		return p.proxyFallback(cli, srv)
	}

	tcli, tsrv, serverName, err := tls.Handshake(slog.GroupValue(slog.Any("proxy", p)), cli, srv)
	if err != nil {
		// If the ephemeral MITM certificate we generated is not recognized, most
		// clients will close the connection during TLS handshake. This probably
		// means: (a) the application is using an unknown CA root store location,
		// (b) it's using certificate pinning, (c) it's an mTLS connection, or (d)
		// something else.
		return fmt.Errorf("proxy tls handshake: %w", err)
	}

	p.tlsServerName.Store(&serverName)
	if err := p.proxyOptimistic(newBufConn(tcli), newBufConn(tsrv)); err != nil {
		return fmt.Errorf("proxy tls: %w", err)
	}

	return nil
}

func (p *proxy) discardMulti(r ...io.Reader) error {
	var wg sync.WaitGroup
	errs := make([]error, len(r), len(r))
	for i := 0; i < len(r); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := io.Copy(io.Discard, r[i]); err != nil {
				errs[i] = fmt.Errorf("discard r[%d]: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// proxyHTTP1 proxies an HTTP connection between the client and server.
func (p *proxy) proxyHTTP1(cli, srv *bufConn) error {
	slog.Debug("starting proxyHTTP1", "proxy", p)

	if !p.isOutgoing && p.global.Devtools != nil && p.global.Devtools.HijackPath != "" {
		lis := newSimpleListener(cli)
		defer lis.Close()

		h := p.newHijacker(srv)

		mp := martian.NewProxy()
		defer mp.Close()

		mp.SetRequestModifier(h)
		mp.SetRoundTripper(h)

		if err := mp.Serve(lis); err != nil {
			return fmt.Errorf("martian: serve: %w", err)
		}
		return nil
	}

	errs := make(chan error, 3)

	cr, cw := io.Pipe()
	sr, sw := io.Pipe()

	go func() {
		bcr, bsr := bufio.NewReader(cr), bufio.NewReader(sr)
		defer p.discardMulti(bcr, bsr)

		for {
			req, err := http.ReadRequest(bcr)
			switch {
			case err == nil:
			case errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed):
				errs <- nil
				return
			default:
				errs <- fmt.Errorf("tracer: read request: %w", err)
				return
			}

			eventID := uuid.New()
			slog.Debug("proxy: http/1: new event", "proxy", p, "eventID", eventID)

			event := p.tmpl.Copy()
			event.Set("event_id", eventID.String())

			parser := tracer.NewParser(p.global, event)
			parser.UseRequest(req)
			go func() {
				defer req.Body.Close()
				io.Copy(io.Discard, req.Body)
			}()

			resp, err := http.ReadResponse(bsr, req)
			switch {
			case err == nil:
			case errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed):
				errs <- nil
				return
			default:
				errs <- fmt.Errorf("tracer: read response: %w", err)
				return
			}

			parser.UseResponse(resp)
			go func() {
				defer resp.Body.Close()
				io.Copy(io.Discard, resp.Body)
			}()

			if resp.StatusCode == http.StatusSwitchingProtocols {
				upgrade := req.Header.Get("upgrade")
				if isWebsocketEnabled && strings.ToLower(upgrade) == "websocket" {
					w, err := newWebsocket(bcr, bsr, resp, p.isOutgoing)
					if err != nil {
						errs <- fmt.Errorf("tracer: create websocket: %w", err)
						return
					}

					msgs, err := w.proxy()
					switch {
					case err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
						parser.UseWebsocketMessages(msgs)
					case errors.Is(err, errWebsocketPayloadLimitExceeded), errors.Is(err, errWebsocketTimeLimitExceeded):
						slog.Debug("tracer: proxy websocket", "err", err)
						parser.UseWebsocketMessages(msgs)
					default:
						errs <- fmt.Errorf("tracer: proxy websocket: %w", err)
						return
					}

					if err := parser.Finish(); err != nil {
						slog.Error("failed to finish HAR parser for websocket", "eventID", event.Get("event_id"), "err", err)
					}
					errs <- nil
					return
				}

				if err := parser.Finish(); err != nil {
					slog.Error(fmt.Sprintf("failed to finish HAR parser for %s", upgrade), "eventID", event.Get("event_id"), "err", err)
				}

				slog.Debug("proxy: dropping into fallback copy after status 101 Switching Protocols", "proxy", p, "eventID", event.Get("event_id"), "tags.http_req_upgrade", req.Header.Get("upgrade"))
				if err := p.discardMulti(bcr, bsr); err != nil {
					errs <- fmt.Errorf("discard after HTTP 101: %w", err)
					return
				}
				errs <- nil
				return
			}

			if err := parser.Finish(); err != nil {
				slog.Error("failed to finish HAR parser for http/1", "eventID", event.Get("event_id"), "err", err)
			}
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

const (
	wsOpcodeContinuation = 0x0
	wsOpcodeText         = 0x1
	wsOpcodeBinary       = 0x2
	wsOpcodeClose        = 0x8
	wsOpcodePing         = 0x9
	wsOpcodePong         = 0xA
)

var errWebsocketPayloadLimitExceeded = errors.New("websocket payload limit exceeded")
var errWebsocketTimeLimitExceeded = errors.New("websocket time limit exceeded")

type websocketFrame struct {
	rsv1 bool
	rsv2 bool
	rsv3 bool
	fin  bool
	op   byte

	payload []byte
}

type websocket struct {
	cli        io.Reader
	srv        io.Reader
	isOutgoing bool

	n atomic.Uint64

	clientNoContextTakeover bool
	serverNoContextTakeover bool
	clientMaxWindowBits     int
	serverMaxWindowBits     int

	clientSlidingWindow []byte
	serverSlidingWindow []byte
	clientInflater      io.ReadCloser
	serverInflater      io.ReadCloser

	mu   sync.RWMutex
	msgs []*tracer.WebsocketMessage
}

func newWebsocket(cli, srv io.Reader, resp *http.Response, isOutgoing bool) (*websocket, error) {
	w := &websocket{
		cli:        cli,
		srv:        srv,
		isOutgoing: isOutgoing,
	}
	w.n.Store(uint64(tracer.PayloadLimitBytes))

	h := resp.Header.Get("sec-websocket-extensions")
	exts := strings.SplitSeq(h, ",")
	for ext := range exts {
		ext = strings.TrimSpace(ext)
		ext, found := strings.CutPrefix(ext, "permessage-deflate")
		if !found {
			continue
		}

		options := strings.SplitSeq(ext, ";")
		for option := range options {
			option = strings.TrimSpace(option)
			switch {
			case option == "client_no_context_takeover":
				w.clientNoContextTakeover = true
			case option == "server_no_context_takeover":
				w.serverNoContextTakeover = true
			case strings.HasPrefix(option, "client_max_window_bits="):
				val := strings.TrimPrefix(option, "client_max_window_bits=")
				n, err := strconv.Atoi(val)
				if err != nil {
					continue
				}
				w.clientMaxWindowBits = n
			case strings.HasPrefix(option, "server_max_window_bits="):
				val := strings.TrimPrefix(option, "server_max_window_bits=")
				n, err := strconv.Atoi(val)
				if err != nil {
					continue
				}
				w.serverMaxWindowBits = n
			}
		}
	}

	// ref: https://datatracker.ietf.org/doc/html/rfc7692#section-7.1.2.1
	//
	// "...Absence of this parameter in an extension negotiation offer indicates
	// that the client can receive messages compressed using an LZ77 sliding
	// window of up to 32,768 bytes." (32768 is 1 << 15)
	if w.clientMaxWindowBits == 0 {
		w.clientMaxWindowBits = 15
	}
	if w.serverMaxWindowBits == 0 {
		w.serverMaxWindowBits = 15
	}

	// ref: https://datatracker.ietf.org/doc/html/rfc7692#section-7.1.2.1
	//
	// "A client MAY include the "server_max_window_bits" extension parameter
	// in an extension negotiation offer.  This parameter has a decimal
	// integer value without leading zeroes between 8 to 15, inclusive..."
	//
	// While being spec compilant, this also ensures that we don't allocate
	// arbitrarily large amounts of memory for the sliding window.
	if w.clientMaxWindowBits < 8 || w.clientMaxWindowBits > 15 {
		return nil, fmt.Errorf("invalid client max window bits: %d", w.clientMaxWindowBits)
	}
	if w.serverMaxWindowBits < 8 || w.serverMaxWindowBits > 15 {
		return nil, fmt.Errorf("invalid server max window bits: %d", w.serverMaxWindowBits)
	}

	return w, nil
}

func (w *websocket) addMessage(op byte, payload []byte, isClient bool, t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	data := ""
	if op == wsOpcodeText {
		data = string(payload)
	} else {
		data = base64.StdEncoding.EncodeToString(payload)
	}

	var dir string
	if (isClient && w.isOutgoing) || (!isClient && !w.isOutgoing) {
		dir = "send"
	} else {
		dir = "receive"
	}

	w.msgs = append(w.msgs, &tracer.WebsocketMessage{
		Type:   dir,
		Time:   float64(t.UnixNano()) / 1e9,
		Opcode: int(op),
		Data:   data,
	})
}

func (w *websocket) getMessages() []*tracer.WebsocketMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]*tracer.WebsocketMessage{}, w.msgs...)
}

// readFull reads len(p) bytes from the client or server reader, enforcing a shared
// byte limit across both. Returns an error if the limit is exceeded. Safe for both
// the client and server to call concurrently.
func (w *websocket) readFull(isClient bool, p []byte) (int, error) {
	l := uint64(len(p))

	for {
		remaining := w.n.Load()
		if remaining < l {
			return 0, fmt.Errorf("%w: limit=%d, have=%d, want=%d, msgsRead=%d", errWebsocketPayloadLimitExceeded, tracer.PayloadLimitBytes, remaining, l, len(w.getMessages()))
		}

		if w.n.CompareAndSwap(remaining, remaining-l) {
			var r io.Reader
			if isClient {
				r = w.cli
			} else {
				r = w.srv
			}

			n, err := io.ReadFull(r, p)
			if err != nil {
				w.n.Add(l - uint64(n))
				return n, err
			}
			return n, nil
		}
	}
}

func (w *websocket) readFrame(isClient bool) (*websocketFrame, error) {
	header := make([]byte, 2)
	if _, err := w.readFull(isClient, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	fin := header[0]&0x80 != 0

	rsv := header[0] & 0x70
	rsv1 := rsv&0x40 != 0
	rsv2 := rsv&0x20 != 0
	rsv3 := rsv&0x10 != 0

	if rsv2 || rsv3 {
		return nil, fmt.Errorf("unsupported extension: RSV1=%t, RSV2=%t, RSV3=%t", rsv1, rsv2, rsv3)
	}

	opcode := header[0] & 0x0F

	masked := header[1]&0x80 != 0
	if isClient && !masked {
		return nil, fmt.Errorf("client sent unmasked frame")
	}

	switch opcode {
	case wsOpcodeContinuation, wsOpcodeText, wsOpcodeBinary, wsOpcodePing, wsOpcodePong, wsOpcodeClose:
	default:
		return nil, fmt.Errorf("invalid opcode: %x", opcode)
	}

	payloadLen := uint64(header[1] & 0x7F)
	if payloadLen == 126 {
		ext := make([]byte, 2)
		if _, err := w.readFull(isClient, ext); err != nil {
			return nil, fmt.Errorf("read extended length (len=126): %w", err)
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext))
	} else if payloadLen == 127 {
		ext := make([]byte, 8)
		if _, err := w.readFull(isClient, ext); err != nil {
			return nil, fmt.Errorf("read extended length (len=127): %w", err)
		}
		payloadLen = binary.BigEndian.Uint64(ext)
	}

	mask := make([]byte, 4)
	if masked {
		if _, err := w.readFull(isClient, mask); err != nil {
			return nil, fmt.Errorf("read mask: %w", err)
		}
	}

	// Check beforehand so we don't allocate arbitrarily large amounts of memory
	// for the payload. All the other reads use fixed size buffers, so they're fine.
	remaining := w.n.Load()
	if remaining < payloadLen {
		return nil, fmt.Errorf("%w: limit=%d, have=%d, want=%d, msgsRead=%d", errWebsocketPayloadLimitExceeded, tracer.PayloadLimitBytes, remaining, payloadLen, len(w.getMessages()))
	}

	payload := make([]byte, payloadLen)
	if _, err := w.readFull(isClient, payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	if rsv1 {
		switch opcode {
		case wsOpcodeBinary, wsOpcodeText:
		default:
			return nil, fmt.Errorf("received rsv1=true with frame opcode %x", opcode)
		}
	}

	return &websocketFrame{
		rsv1:    rsv1,
		rsv2:    rsv2,
		rsv3:    rsv3,
		fin:     fin,
		op:      opcode,
		payload: payload,
	}, nil
}

func (w *websocket) cleanup() {
	if w.clientInflater != nil {
		w.clientInflater.Close()
	}

	if w.serverInflater != nil {
		w.serverInflater.Close()
	}
}

func (w *websocket) decompress(b []byte, isClient bool) ([]byte, error) {
	bCopy := append([]byte{}, b...)

	// ref: https://datatracker.ietf.org/doc/html/rfc7692#section-7.2.2
	bCopy = append(bCopy, 0x00, 0x00, 0xff, 0xff)

	var inflaterPtr *io.ReadCloser
	var noCtx bool
	var bits int
	var window *[]byte

	if isClient {
		noCtx = w.clientNoContextTakeover
		inflaterPtr = &w.clientInflater
		bits = w.clientMaxWindowBits
		window = &w.clientSlidingWindow
	} else {
		noCtx = w.serverNoContextTakeover
		inflaterPtr = &w.serverInflater
		bits = w.serverMaxWindowBits
		window = &w.serverSlidingWindow
	}

	var r io.ReadCloser
	if noCtx {
		r = flate.NewReader(bytes.NewReader(bCopy))
		defer r.Close()
	} else {
		if *inflaterPtr == nil {
			r = flate.NewReaderDict(bytes.NewReader(bCopy), *window)
			*inflaterPtr = r
		} else {
			r = *inflaterPtr
			zr, ok := r.(flate.Resetter)
			if !ok {
				return nil, fmt.Errorf("inflater does not implement flate.Resetter")
			}
			if err := zr.Reset(bytes.NewReader(bCopy), *window); err != nil {
				return nil, fmt.Errorf("reset: %w", err)
			}
		}
	}

	data, err := io.ReadAll(r)
	switch {
	case err == nil || errors.Is(err, io.ErrUnexpectedEOF):
		// Not all websocket implementations send a properly terminated DEFLATE stream,
		// which might cause an io.ErrUnexpectedEOF. Go's flate.NewReader (correctly)
		// treats these cases as errors, but that's just how real implementations behave.
		if !noCtx {
			maxSize := 1 << bits
			if len(data) >= maxSize {
				*window = append([]byte{}, data[len(data)-maxSize:]...)
			} else {
				totalLen := len(*window) + len(data)
				if totalLen <= maxSize {
					*window = append(*window, data...)
				} else {
					newWindow := make([]byte, 0, maxSize)
					newWindow = append(newWindow, (*window)[totalLen-maxSize:]...)
					newWindow = append(newWindow, data...)
					*window = newWindow
				}
			}
		}
		return data, nil
	default:
		return nil, fmt.Errorf("read: %w", err)
	}
}

func (w *websocket) proxy() ([]*tracer.WebsocketMessage, error) {
	defer w.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), websocketTimeLimit)
	defer cancel()
	errs := make(chan error, 2)

	readLoop := func(isClient bool) {
		var payload []byte
		var op byte
		var t time.Time
		var compressed bool
		first := true

		type frameResult struct {
			frame *websocketFrame
			err   error
		}
		for {
			ch := make(chan frameResult, 1)
			go func() {
				frame, err := w.readFrame(isClient)
				select {
				case <-ctx.Done():
				case ch <- frameResult{frame, err}:
				}
			}()

			var frame *websocketFrame
			select {
			case <-ctx.Done():
				return
			case result := <-ch:
				if result.err != nil {
					errs <- fmt.Errorf("read frame (isClient=%t): %w", isClient, result.err)
					return
				}
				frame = result.frame
			}

			switch frame.op {
			case wsOpcodeContinuation:
				if first {
					errs <- fmt.Errorf("unexpected continuation frame (isClient=%t)", isClient)
					return
				}
				payload = append(payload, frame.payload...)
				if frame.fin {
					if compressed {
						var err error
						payload, err = w.decompress(payload, isClient)
						if err != nil {
							errs <- fmt.Errorf("decompress: %w", err)
							return
						}
					}
					w.addMessage(op, payload, isClient, t)
					payload = nil
					first = true
				}
			case wsOpcodeText, wsOpcodeBinary:
				if !first {
					errs <- fmt.Errorf("received new data frame before previous message finished (isClient=%t)", isClient)
					return
				}
				t = time.Now()
				op = frame.op
				compressed = frame.rsv1

				payload = append(payload, frame.payload...)
				first = frame.fin
				if frame.fin {
					if compressed {
						var err error
						payload, err = w.decompress(payload, isClient)
						if err != nil {
							errs <- fmt.Errorf("decompress: %w", err)
							return
						}
					}

					w.addMessage(op, payload, isClient, t)
					payload = nil
				}
			case wsOpcodePing, wsOpcodePong, wsOpcodeClose:
				if !frame.fin {
					errs <- fmt.Errorf("received fragmented control frame (isClient=%t)", isClient)
					return
				}

				if frame.op == wsOpcodeClose {
					errs <- nil
					return
				}

				continue
			default:
				errs <- fmt.Errorf("unknown opcode (isClient=%t): %x", isClient, frame.op)
				return
			}
		}
	}

	go readLoop(true)
	go readLoop(false)

	select {
	case err1 := <-errs:
		if err1 != nil {
			cancel()
			<-ctx.Done()
			return w.getMessages(), fmt.Errorf("read loop: %w", err1)
		}

		err2 := <-errs
		if err2 != nil {
			return w.getMessages(), fmt.Errorf("read loop: %w", err2)
		}

		return w.getMessages(), nil
	case <-ctx.Done():
		return w.getMessages(), fmt.Errorf("%w: bytesRead=%d, limit=%d, msgsRead=%d", errWebsocketTimeLimitExceeded, uint64(tracer.PayloadLimitBytes)-w.n.Load(), tracer.PayloadLimitBytes, len(w.getMessages()))
	}
}

type http2Stream struct {
	streamID uint32

	event  *event.Event
	parser *tracer.Parser

	active sync.WaitGroup

	req struct {
		Request      *http.Request
		buf          *io.PipeWriter
		headersEnded bool
	}

	resp struct {
		Response     *http.Response
		buf          *io.PipeWriter
		headersEnded bool
	}
}

func (p *proxy) newHTTP2Stream(streamID uint32) *http2Stream {
	eventID := uuid.New()
	slog.Debug("proxy: http/2: new event", "proxy", p, "eventID", eventID)

	event := p.global.Config.GetEventTemplate()
	event.Set("event_id", eventID.String())

	st := new(http2Stream)
	st.streamID = streamID

	st.event = event
	st.parser = tracer.NewParser(p.global, event)

	st.active.Add(2)

	st.req.Request = new(http.Request)
	st.req.Request.Proto = "HTTP/2"
	st.req.Request.ProtoMajor = 2
	st.req.Request.ProtoMinor = 0
	st.req.Request.URL = new(url.URL)
	st.req.Request.Header = make(http.Header)
	st.req.Request.Trailer = make(http.Header)
	st.req.Request.Body, st.req.buf = io.Pipe()

	st.resp.Response = new(http.Response)
	st.resp.Response.Proto = "HTTP/2"
	st.resp.Response.ProtoMajor = 2
	st.resp.Response.ProtoMinor = 0
	st.resp.Response.Header = make(http.Header)
	st.resp.Response.Trailer = make(http.Header)
	st.resp.Response.Body, st.resp.buf = io.Pipe()

	go func() {
		st.active.Wait()
		if err := st.parser.Finish(); err != nil {
			slog.Error("failed to finish HAR parser", "eventID", st.event.Get("event_id"), "err", err)
		}
	}()

	return st
}

func (p *proxy) proxyHTTP2(cli, srv *bufConn) error {
	slog.Debug("starting proxyHTTP2", "proxy", p)

	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(cli, preface); err != nil {
		return fmt.Errorf("read preface: %w", err)
	}
	if string(preface) != http2.ClientPreface {
		return fmt.Errorf("bad preface")
	}
	if _, err := srv.Write(preface); err != nil {
		return fmt.Errorf("write preface: %w", err)
	}

	var mu sync.Mutex
	state := make(map[uint32]*http2Stream)

	getStream := func(streamID uint32) *http2Stream {
		mu.Lock()
		defer mu.Unlock()

		st, ok := state[streamID]
		if !ok {
			st = p.newHTTP2Stream(streamID)
			state[streamID] = st
			go func() {
				st.active.Wait()
				mu.Lock()
				defer mu.Unlock()
				delete(state, streamID)
			}()
		}
		return st
	}

	copySingle := func(dst *http2.Framer, src *http2.Framer, isClient bool) error {
		dec := hpack.NewDecoder(4096, nil)
		for {
			fr, err := src.ReadFrame()
			switch {
			case err == nil:
			case errors.Is(err, io.EOF):
				return nil
			case errors.Is(err, net.ErrClosed):
				return nil
			case strings.Contains(err.Error(), "connection reset by peer"):
				return nil
			default:
				return fmt.Errorf("read frame: %w", err)
			}

			switch fr := fr.(type) {
			case *http2.HeadersFrame:
				st := getStream(fr.StreamID)

				headers, err := dec.DecodeFull(fr.HeaderBlockFragment())
				if err != nil {
					return fmt.Errorf("%T: decode fields: %w", fr, err)
				}

				var isTrailer bool
				if isClient {
					isTrailer = st.req.headersEnded
				} else {
					isTrailer = st.resp.headersEnded
				}

				for _, hdr := range headers {
					switch hdr.Name {
					case ":method":
						st.req.Request.Method = hdr.Value
					case ":path":
						st.req.Request.URL.RawPath = hdr.Value
						st.req.Request.URL.Path = hdr.Value
					case ":scheme":
					case ":authority":
					case ":status":
						code := 0
						for i := 0; i < len(hdr.Value); i++ {
							if hdr.Value[i] < '0' || hdr.Value[i] > '9' {
								break
							}
							code *= 10
							code += int(byte(hdr.Value[i])) - int(byte('0'))
						}
						st.resp.Response.StatusCode = code
						st.resp.Response.Status = http.StatusText(code)
					default:
						if !isTrailer {
							if isClient {
								st.req.Request.Header.Add(hdr.Name, hdr.Value)
							} else {
								st.resp.Response.Header.Add(hdr.Name, hdr.Value)
							}
						} else {
							if isClient {
								st.req.Request.Trailer.Add(hdr.Name, hdr.Value)
							} else {
								st.resp.Response.Trailer.Add(hdr.Name, hdr.Value)
							}
						}
					}
				}

				if fr.HeadersEnded() {
					if !isTrailer {
						if isClient {
							st.req.headersEnded = true
						} else {
							st.resp.headersEnded = true
						}
					}
				}

				if fr.HeadersEnded() {
					if !isTrailer {
						if isClient {
							st.parser.UseRequest(st.req.Request)
							go func() {
								defer st.req.Request.Body.Close()
								io.Copy(io.Discard, st.req.Request.Body)
							}()
						} else {
							st.parser.UseResponse(st.resp.Response)
							go func() {
								defer st.resp.Response.Body.Close()
								io.Copy(io.Discard, st.resp.Response.Body)
							}()
						}
					} else {
						if isClient {
							st.parser.SetRequestTrailer(st.req.Request.Trailer)
						} else {
							st.parser.SetResponseTrailer(st.resp.Response.Trailer)
						}
					}
				}

				if fr.StreamEnded() {
					if isClient {
						st.req.buf.Close()
					} else {
						st.resp.buf.Close()
					}
					st.active.Done()
				}

				p := http2.HeadersFrameParam{
					StreamID:      fr.StreamID,
					BlockFragment: fr.HeaderBlockFragment(),
					EndStream:     fr.StreamEnded(),
					EndHeaders:    fr.HeadersEnded(),
					Priority:      fr.Priority,
				}
				if err := dst.WriteHeaders(p); err != nil {
					return fmt.Errorf("%T: write headers: %w", fr, err)
				}

			case *http2.DataFrame:
				st := getStream(fr.StreamID)
				if err := dst.WriteData(fr.StreamID, fr.StreamEnded(), fr.Data()); err != nil {
					return fmt.Errorf("%T: write data: %w", fr, err)
				}

				if isClient {
					if _, err := st.req.buf.Write(fr.Data()); err != nil {
						return fmt.Errorf("%T: write pipe: %w", fr, err)
					}
				} else {
					if _, err := st.resp.buf.Write(fr.Data()); err != nil {
						return fmt.Errorf("%T: write pipe: %w", fr, err)
					}
				}

				if fr.StreamEnded() {
					if isClient {
						st.req.buf.Close()
					} else {
						st.resp.buf.Close()
					}
					st.active.Done()
				}

			case *http2.SettingsFrame:
				if fr.IsAck() {
					if err := dst.WriteSettingsAck(); err != nil {
						return fmt.Errorf("%T: write settings ack: %w", fr, err)
					}
				} else {
					var arr []http2.Setting
					for i := 0; i < fr.NumSettings(); i++ {
						arr = append(arr, fr.Setting(i))
					}
					if err := dst.WriteSettings(arr...); err != nil {
						return fmt.Errorf("%T: forward: %w", fr, err)
					}
				}

			case *http2.PingFrame:
				if err := dst.WritePing(fr.Flags.Has(http2.FlagPingAck), fr.Data); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			case *http2.WindowUpdateFrame:
				if err := dst.WriteWindowUpdate(fr.StreamID, fr.Increment); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			case *http2.RSTStreamFrame:
				if err := dst.WriteRSTStream(fr.StreamID, fr.ErrCode); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			case *http2.GoAwayFrame:
				if err := dst.WriteGoAway(fr.LastStreamID, fr.ErrCode, fr.DebugData()); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			case *http2.PriorityFrame:
				if err := dst.WritePriority(fr.StreamID, fr.PriorityParam); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			case *http2.PushPromiseFrame:
				param := http2.PushPromiseParam{
					StreamID:      fr.StreamID,
					PromiseID:     fr.PromiseID,
					BlockFragment: fr.HeaderBlockFragment(),
					EndHeaders:    fr.HeadersEnded(),
				}
				if err := dst.WritePushPromise(param); err != nil {
					return fmt.Errorf("%T: forward: %w", fr, err)
				}

			default:
			}
		}
	}

	errs := make(chan error, 2)

	go func() {
		defer srv.CloseWrite()
		defer cli.CloseRead()
		if err := copySingle(http2.NewFramer(srv, nil), http2.NewFramer(nil, cli), true); err != nil {
			errs <- fmt.Errorf("client->server: %w", err)
			return
		}
		errs <- nil
	}()

	go func() {
		defer srv.CloseWrite()
		defer cli.CloseRead()
		if err := copySingle(http2.NewFramer(cli, nil), http2.NewFramer(nil, srv), false); err != nil {
			errs <- fmt.Errorf("server->client: %w", err)
			return
		}
		errs <- nil
	}()

	if err := errors.Join(<-errs, <-errs); err != nil {
		return fmt.Errorf("http/2 proxy: %w", err)
	}
	return nil
}

func (p *proxy) proxyFallback(cli, srv *bufConn) error {
	slog.Debug("starting proxyFallback", "proxy", p)

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

type simpleListener struct {
	conn   net.Conn
	ch     chan net.Conn
	closed atomic.Bool
}

var _ net.Listener = new(simpleListener)

func newSimpleListener(conn net.Conn) *simpleListener {
	ch := make(chan net.Conn, 1)
	ch <- conn
	return &simpleListener{conn: conn, ch: ch}
}

func (l *simpleListener) Accept() (net.Conn, error) {
	if conn, ok := <-l.ch; ok {
		return conn, nil
	}
	return nil, fmt.Errorf("listener: closed")
}

func (l *simpleListener) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return fmt.Errorf("listener: already closed")
	}

	close(l.ch)
	return nil
}

func (l *simpleListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

type hijacker struct {
	proxy *proxy
	begin time.Time
	srv   net.Conn
}

var _ http.RoundTripper = new(hijacker)

func (p *proxy) newHijacker(srv net.Conn) *hijacker {
	return &hijacker{
		proxy: p,
		begin: time.Now(),
		srv:   srv,
	}
}

func (h *hijacker) ModifyRequest(req *http.Request) error {
	if req.URL.Path == h.proxy.global.Devtools.HijackPath {
		// We need to serve the devtools bundle by hijacking the client-side
		// connection. As a result, this proxy will no longer be used for regular
		// HTTP requests (i.e. non-devtools paths). Some programs such as Python's
		// SimpleHTTPServer are single-threaded, which means they will be blocked
		// until the last open connection finishes. As a result, we must first
		// close the server-side connection before starting the Martian hijack.
		h.srv.Close()

		conn, brw, err := martian.NewContext(req).Session().Hijack()
		if err != nil {
			return fmt.Errorf("subtrace: failed to hijack devtools endpoint: %w", err)
		}
		h.proxy.global.Devtools.HandleHijack(req, conn, brw)
	}

	return nil
}

func (h *hijacker) RoundTrip(req *http.Request) (*http.Response, error) {
	event := h.proxy.global.Config.GetEventTemplate()
	event.Set("event_id", uuid.New().String())

	parser := tracer.NewParser(h.proxy.global, event)
	parser.UseRequest(req)

	tr := &http.Transport{
		DisableKeepAlives: true,

		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return h.srv, nil
		},
		Dial: func(network string, addr string) (net.Conn, error) {
			return h.srv, nil
		},
		DialTLSContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return nil, fmt.Errorf("invalid use of DialTLSContext in simpleRoundTripper")
		},
		DialTLS: func(network string, addr string) (net.Conn, error) {
			return nil, fmt.Errorf("invalid use of DialTLS in simpleRoundTripper")
		},
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	parser.UseResponse(resp)
	go func() {
		if err := parser.Finish(); err != nil {
			slog.Error("failed to finish HAR parser", "eventID", event.Get("event_id"), "err", err)
		}
	}()

	return resp, err
}
