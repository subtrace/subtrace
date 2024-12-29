// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package web

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"nhooyr.io/websocket"
)

func fallback(msg string) []byte {
	b := new(bytes.Buffer)
	fmt.Fprintf(b, `<!DOCTYPE html>`)
	fmt.Fprintf(b, `<html>`)
	fmt.Fprintf(b, `<head><meta charset="utf-8"><title>Subtrace</title></head>`)
	fmt.Fprintf(b, `<style>*    { box-sizing: border-box; }</style>`)
	fmt.Fprintf(b, `<style>body { margin: 2rem 1rem; display: flex; justify-content: center; font-family: monospace; font-size: 11px; }</style>`)
	fmt.Fprintf(b, `<style>div  { padding: 1rem; width: 100%%; max-width: 24rem; border: 1px solid #00000020; border-radius: 2px; text-align: center; }</style>`)
	fmt.Fprintf(b, `<body><div>%s</div></body>`, msg)
	fmt.Fprintf(b, `</html>`)
	return b.Bytes()
}

//go:embed bundle
var bundle embed.FS

//go:embed init.js
var initJS []byte

var html []byte

var once sync.Once

func bundleOnce() {
	once.Do(func() {
		f, err := bundle.Open("bundle/devtools.html.gz")
		if err != nil {
			html = fallback("This build of subtrace was compiler without Chrome DevTools support.")
			return
		}
		defer f.Close()

		r, err := gzip.NewReader(f)
		if err != nil {
			html = fallback(fmt.Sprintf("Subtrace failed to extract the gzip web bundle.<br><br>Error: %q", fmt.Errorf("create: %w", err)))
			return
		}

		html, err = io.ReadAll(r)
		if err != nil {
			html = fallback(fmt.Sprintf("Subtrace failed to extract the gzip web bundle.<br><br>Error: %q", fmt.Errorf("read: %w", err)))
			return
		}

		html = bytes.ReplaceAll(html, []byte("__SUBTRACE_INIT__"), initJS)
	})
}

type Server struct {
	mu    sync.Mutex
	conns []*websocket.Conn
}

var _ http.Handler = new(Server)

func NewServer() *Server {
	return &Server{}
}

func (s *Server) add(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = append(s.conns, conn)
}

func (s *Server) remove(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conns {
		if s.conns[i] == conn {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			return
		}
	}
}

func (s *Server) snapshot() []*websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*websocket.Conn{}, s.conns...)
}

func (s *Server) Send(b []byte) {
	var wg sync.WaitGroup
	for _, conn := range s.snapshot() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn.Write(context.Background(), websocket.MessageBinary, b)
		}()
	}
	wg.Wait()
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		w.Header().Set("content-type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to accept websocket: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	s.add(conn)
	defer s.remove(conn)

	go func() {
		for {
			if _, msg, err := conn.Read(r.Context()); err != nil {
				return
			} else {
				fmt.Printf("received: %s\n", msg)
			}
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := conn.Ping(r.Context()); err != nil {
				return
			}
		}
	}
}

func (s *Server) html(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "max-age=3600")
	w.Header().Set("cross-origin-opener-policy", "same-origin")
	w.Header().Set("cross-origin-embedder-policy", "require-corp")

	accept := make(map[string]bool)
	for _, val := range strings.Split(r.Header.Get("accept-encoding"), ",") {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		accept[val] = true
	}

	var body io.Writer = w
	switch {
	case accept["br"]:
		w.Header().Set("content-encoding", "br")
		bw := brotli.NewWriter(w)
		defer bw.Close()
		body = bw
	case accept["gzip"]:
		w.Header().Set("content-encoding", "gzip")
		gw := gzip.NewWriter(w)
		defer gw.Close()
		body = gw
	}

	bundleOnce()
	body.Write(html)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("upgrade") == "websocket" {
		s.websocket(w, r)
	} else {
		s.html(w, r)
	}
}

func (s *Server) HandleHijack(req *http.Request, conn net.Conn, brw *bufio.ReadWriter) {
	defer conn.Close()

	prw := newPipeResponseWriter(conn, brw)
	defer prw.finish()

	s.ServeHTTP(prw, req)
	slog.Debug("web server finished handling hijacked devtools endpoint request", "path", req.URL.Path, "upgrade", req.Header.Get("upgrade"))
}

type pipeResponseWriter struct {
	conn         net.Conn
	brw          *bufio.ReadWriter
	pw           *io.PipeWriter
	errs         chan error
	done         chan struct{}
	resp         *http.Response
	wroteHeader  bool
	sentResponse bool
	hasContent   bool
	wasHijacked  bool
}

var _ http.ResponseWriter = new(pipeResponseWriter)

func newPipeResponseWriter(conn net.Conn, brw *bufio.ReadWriter) *pipeResponseWriter {
	pr, pw := io.Pipe()
	return &pipeResponseWriter{
		conn: conn,
		brw:  brw,
		pw:   pw,
		errs: make(chan error, 1),
		done: make(chan struct{}),
		resp: &http.Response{
			Proto:      "HTTP/1.0",
			ProtoMajor: 1,
			ProtoMinor: 0,
			Header:     make(http.Header),
			Body:       pr,
			Close:      true,
		},
	}
}

func (w *pipeResponseWriter) finish() {
	if w.wasHijacked {
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusInternalServerError)
	}
	if !w.sentResponse {
		w.sendResponse()
	}
	w.pw.Close()
	<-w.done
}

func (w *pipeResponseWriter) sendResponse() {
	if w.sentResponse {
		return
	}
	w.sentResponse = true

	if w.hasContent {
		w.resp.ContentLength = -1
	}

	go func() {
		defer close(w.done)
		w.errs <- w.resp.Write(w.conn)
	}()
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.resp.Header
}

func (w *pipeResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true

	w.resp.StatusCode = code
	w.resp.Status = fmt.Sprintf("%d %s", code, http.StatusText(code))
}

func (w *pipeResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	w.hasContent = true
	if !w.sentResponse {
		w.sendResponse()
	}
	return w.pw.Write(b)
}

func (w *pipeResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.wasHijacked {
		return nil, nil, fmt.Errorf("already hijacked")
	}
	w.wasHijacked = true

	if w.wroteHeader && !w.sentResponse {
		w.pw.Close()
		w.sendResponse()
		<-w.done
	}
	return w.conn, w.brw, nil
}
