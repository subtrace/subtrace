// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package worker

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"subtrace.dev/cli/cmd/worker/clickhouse"
	"subtrace.dev/cli/journal"

	"github.com/golang/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/google/uuid"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type decoder struct {
	r  io.Reader
	ch *clickhouse.Client

	conn map[uuid.UUID]struct{}
	http map[uuid.UUID]struct{}
}

func newDecoder(r io.Reader, c *clickhouse.Client) *decoder {
	return &decoder{
		r:    r,
		ch:   c,
		conn: make(map[uuid.UUID]struct{}),
		http: make(map[uuid.UUID]struct{}),
	}
}

func (d *decoder) open(httpID uuid.UUID, isRequest bool, flag int) (*os.File, error) {
	path := filepath.Join(os.TempDir(), "subtrace", "worker", "http")
	if flag&os.O_CREATE != 0 {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
	}

	if isRequest {
		path = filepath.Join(path, httpID.String()+".req")
	} else {
		path = filepath.Join(path, httpID.String()+".resp")
	}

	return os.OpenFile(path, flag, 0o644)
}

func (d *decoder) decode(ctx context.Context) error {
	magic := make([]byte, len(journal.Magic))
	if _, err := io.ReadFull(d.r, magic); err != nil {
		return fmt.Errorf("read journal magic: %w", err)
	}
	if !bytes.Equal(magic, journal.Magic[:]) {
		return fmt.Errorf("malformed journal: invalid magic: got %016x, want %016x", magic, journal.Magic)
	}

loop:
	for i := 0; ; i++ {
		hdr := make([]byte, 20)
		if _, err := io.ReadFull(d.r, hdr); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("page[%d]: read header: %w", i, err)
		}

		size := int(binary.BigEndian.Uint16(hdr[0:2]))
		t := journal.PageType(binary.BigEndian.Uint16(hdr[2:4]))
		var id uuid.UUID
		copy(id[:], hdr[4:])

		if i == 0 && t != journal.PageType_JOURNAL_BEGIN {
			return fmt.Errorf("page[%d]: malformed journal: got page type %s, want JOURNAL_BEGIN", i, t.String())
		}

		var b []byte
		if size > 0 {
			b = make([]byte, size)
			if _, err := io.ReadFull(d.r, b); err != nil {
				return fmt.Errorf("page[%d]: read %d bytes: %w", i, size, err)
			}
		}

		var err error
		switch t {
		case journal.PageType_JOURNAL_BEGIN:
		case journal.PageType_JOURNAL_END:
			break loop
		case journal.PageType_CONN_BEGIN:
			err = d.parseConnBegin(ctx, id, b)
		case journal.PageType_CONN_END:
			err = d.parseConnEnd(ctx, id, b)
		case journal.PageType_HTTP_BEGIN_REQUEST:
			err = d.parseHTTPBegin(ctx, id, true, b)
		case journal.PageType_HTTP_BEGIN_RESPONSE:
			err = d.parseHTTPBegin(ctx, id, false, b)
		case journal.PageType_HTTP_DATA_REQUEST:
			err = d.parseHTTPData(ctx, id, b, true)
		case journal.PageType_HTTP_DATA_RESPONSE:
			err = d.parseHTTPData(ctx, id, b, false)
		case journal.PageType_HTTP_END_REQUEST:
			err = d.parseHTTPEnd(ctx, id, true, b)
		case journal.PageType_HTTP_END_RESPONSE:
			err = d.parseHTTPEnd(ctx, id, false, b)
		}
		if err != nil {
			return fmt.Errorf("page[%d]: parse %s: %w", i, t.String(), err)
		}
	}

	return nil
}

func (d *decoder) parseConnBegin(ctx context.Context, connID uuid.UUID, b []byte) error {
	var m journal.ConnBegin
	if err := proto.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	var keys []string
	var vals []any
	for k, v := range map[string]any{
		"conn_id": connID,

		"tcp_socket_type":      m.TcpInfo.SocketType.String(),
		"tcp_create_time":      m.TcpInfo.CreateTime,
		"tcp_connect_time":     m.TcpInfo.ConnectTime,
		"tcp_tracee_bind_addr": m.TcpInfo.TraceeBindAddr,
		"tcp_remote_peer_addr": m.TcpInfo.RemotePeerAddr,

		"tls_handshake_begin_time": m.TlsInfo.HandshakeBeginTime,
		"tls_handshake_end_time":   m.TlsInfo.HandshakeEndTime,
		"tls_server_name":          m.TlsInfo.ServerName,
		"tls_negotiated_version":   m.TlsInfo.NegotiatedVersion,
	} {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	if err := d.ch.Exec(ctx, fmt.Sprintf("INSERT INTO conns(%s) VALUES(%s);", strings.Join(keys, ","), strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")), vals...); err != nil {
		return fmt.Errorf("INSERT conns: %w", err)
	}

	d.conn[connID] = struct{}{}
	return nil
}

func (d *decoder) parseConnEnd(ctx context.Context, connID uuid.UUID, b []byte) error {
	var m journal.ConnEnd
	if err := proto.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	if err := d.ch.Exec(ctx, `
		ALTER TABLE conns UPDATE
			tcp_close_time = $2
		WHERE conn_id = $1;
	`, connID, m.TcpCloseTime); err != nil {
		return fmt.Errorf("UPDATE conns: %w", err)
	}

	d.conn[connID] = struct{}{}
	return nil
}

func (d *decoder) parseHTTPBegin(ctx context.Context, httpID uuid.UUID, isRequest bool, b []byte) error {
	var m journal.HTTPBegin
	if err := proto.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	var connID uuid.UUID
	copy(connID[:], m.ConnId)

	var keys []string
	var vals []any
	for k, v := range map[string]any{
		"http_id":      httpID,
		"conn_id":      connID,
		"http_version": m.HttpVersion.String(),
		"begin_time":   m.BeginTime,
	} {
		keys = append(keys, k)
		vals = append(vals, v)
	}

	table := "requests"
	if !isRequest {
		table = "responses"
	}

	q := fmt.Sprintf("INSERT INTO %s(%s) VALUES(%s);", table, strings.Join(keys, ","), strings.TrimSuffix(strings.Repeat("?,", len(keys)), ","))
	if err := d.ch.Exec(ctx, q, vals...); err != nil {
		return fmt.Errorf("INSERT %s: %w", table, err)
	}

	f, err := d.open(httpID, isRequest, os.O_TRUNC|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	defer f.Close()

	d.http[httpID] = struct{}{}
	return nil
}

var pool = sync.Pool{
	New: func() any { return make([]byte, 16<<10) },
}

func (d *decoder) parseHTTPData(ctx context.Context, httpID uuid.UUID, b []byte, isRequest bool) error {
	f, err := d.open(httpID, isRequest, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	dec := pool.Get().([]byte)
	defer pool.Put(dec)

	if dec, err = snappy.Decode(dec, b); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if _, err := f.Write(dec); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	d.http[httpID] = struct{}{}
	return nil
}

func (d *decoder) parseHTTPEnd(ctx context.Context, httpID uuid.UUID, isRequest bool, b []byte) error {
	var m journal.HTTPEnd
	if err := proto.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	table := "requests"
	if !isRequest {
		table = "responses"
	}

	var version string
	if err := d.ch.QueryRow(ctx, `
		SELECT http_version
		FROM `+table+`
		WHERE http_id = $1;
	`, httpID).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("unknown request ID %v", httpID)
	} else if err != nil {
		return fmt.Errorf("SELECT %s: %w", table, err)
	}

	var parser func(context.Context, uuid.UUID, bool) (map[string]any, error)
	var kv map[string]any
	switch version {
	case journal.HTTPVersion_HTTP_1_0.String():
		parser = d.parseHTTP1
	case journal.HTTPVersion_HTTP_1_1.String():
		parser = d.parseHTTP1
	case journal.HTTPVersion_HTTP_2.String():
		parser = d.parseHTTP2
	case journal.HTTPVersion_HTTP_3.String():
		return fmt.Errorf("HTTP/3 unimplemented")
	default:
		return fmt.Errorf("unknown HTTP version %q", version)
	}

	kv, err := parser(ctx, httpID, isRequest)
	if err != nil {
		return fmt.Errorf("parse %s: isRequest=%v: %w", version, isRequest, err)
	}
	kv["end_time"] = m.EndTime

	var cols []string
	var vals []any
	for k, v := range kv {
		cols = append(cols, fmt.Sprintf("%s = ?", k))
		vals = append(vals, v)
	}

	q := fmt.Sprintf("ALTER TABLE %s UPDATE %s WHERE http_id = ?;", table, strings.Join(cols, ","))
	if err := d.ch.Exec(ctx, q, append(vals, httpID)...); err != nil {
		return fmt.Errorf("UPDATE %s: %w", table, err)
	}

	d.http[httpID] = struct{}{}
	return nil
}

func (d *decoder) parseHTTP1(ctx context.Context, httpID uuid.UUID, isRequest bool) (map[string]any, error) {
	f, err := d.open(httpID, isRequest, os.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("open request file: %w", err)
	}
	defer f.Close()
	// defer os.Remove(f.Name())

	kv := make(map[string]any)
	tp := textproto.NewReader(bufio.NewReader(f))
	for i := 0; ; i++ {
		line, err := tp.ReadLine()
		if errors.Is(err, io.EOF) {
			return kv, nil
		} else if err != nil {
			return nil, fmt.Errorf("read line %d: %w", i, err)
		}

		switch {
		case line == "":
			return kv, nil

		case i == 0 && isRequest:
			method, rest, ok := strings.Cut(line, " ")
			if !ok {
				return nil, fmt.Errorf("invalid method line: first split")
			}
			path, _, ok := strings.Cut(rest, " ")
			if !ok {
				return nil, fmt.Errorf("invalid method line: second split")
			}
			mapRequestKey(kv, ":method", method)
			mapRequestKey(kv, ":path", path)

		case i == 0 && !isRequest:
			_, status, ok := strings.Cut(line, " ")
			if !ok {
				return nil, fmt.Errorf("invalid status line: first split")
			}
			code, _, ok := strings.Cut(status, " ")
			if !ok {
				return nil, fmt.Errorf("invalid status line: second split")
			}
			n, err := strconv.Atoi(code)
			if err != nil {
				return nil, fmt.Errorf("invalid status line: parse status code: %w", err)
			}
			mapResponseKey(kv, ":status", n)

		default:
			name, value, ok := strings.Cut(line, ": ")
			if !ok {
				return nil, fmt.Errorf("line %d: invalid header: missing colon", i)
			}
			if isRequest {
				mapRequestKey(kv, name, value)
			} else {
				mapResponseKey(kv, name, value)
			}
		}
	}
}

func (d *decoder) parseHTTP2(ctx context.Context, httpID uuid.UUID, isRequest bool) (map[string]any, error) {
	f, err := d.open(httpID, isRequest, os.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	defer os.Remove(f.Name())

	framer := http2.NewFramer(nil, f)
	if err != nil {
		return nil, fmt.Errorf("new framer: %w", err)
	}

	kv := make(map[string]any)
	for i := 0; ; i++ {
		f, err := framer.ReadFrame()
		if errors.Is(err, io.EOF) {
			return kv, nil
		} else if err != nil {
			return nil, fmt.Errorf("read frame: %w", err)
		}

		switch f.Header().Type {
		case http2.FrameHeaders:
			b := f.(*http2.HeadersFrame).HeaderBlockFragment()
			var errs []error
			if _, err := hpack.NewDecoder(1<<16, func(h hpack.HeaderField) {
				var value any
				switch {
				case !isRequest && h.Name == ":status":
					n, err := strconv.Atoi(h.Value)
					if err != nil {
						errs = append(errs, fmt.Errorf("parse status code: %w", err))
						return
					}
					value = n
				default:
					value = h.Value
				}

				if isRequest {
					mapRequestKey(kv, h.Name, value)
				} else {
					mapResponseKey(kv, h.Name, value)
				}
			}).Write(b); err != nil {
				return nil, fmt.Errorf("hpack decode: %w", err)
			}
			if len(errs) > 0 {
				return nil, fmt.Errorf("hpack map: %w", errors.Join(errs...))
			}
		}
	}
}

func mapRequestKey(kv map[string]any, name string, value any) {
	mapping := map[string]string{
		":method":    "method",
		":path":      "path",
		"user-agent": "user_agent",
	}

	if col, ok := mapping[strings.ToLower(name)]; ok {
		kv[col] = value
	}
}

func mapResponseKey(kv map[string]any, name string, value any) {
	mapping := map[string]string{
		":status": "status_code",
	}

	if col, ok := mapping[strings.ToLower(name)]; ok {
		kv[col] = value
	}
}
