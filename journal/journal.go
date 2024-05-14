// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/snappy"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
	"subtrace.dev/rpc"
)

var Default *Journal

func Init() error {
	path := filepath.Join(os.TempDir(), "subtrace", "tracer", "journals")
	j, err := newJournal(path)
	if err != nil {
		return err
	}

	Default = j
	return nil
}

var Magic = [8]byte{'s', 'u', 'b', 't', 'j', 'r', 'n', 'l'}

const (
	journalMaxFileSize    = 64 << 20   // advance to a new file when the current journal >= ~64MB
	journalPageHeaderSize = 2 + 2 + 16 // page body size + page type + context uuid
	journalTruncateSize   = 128 << 10  // truncate each side of each request at ~128KB (unlikely)
	journalMaxPageSize    = 16 << 10
)

type Journal struct {
	dir  string
	pool sync.Pool

	mu    sync.Mutex
	curID uuid.UUID
	f     *os.File
	w     *bufio.Writer
	n     int
}

func newJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	return &Journal{
		dir:  dir,
		pool: sync.Pool{New: func() any { return make([]byte, journalMaxPageSize) }},
	}, nil
}

func (j *Journal) flushResetLocked(async bool) error {
	if j.w == nil {
		return nil
	}

	if err := j.w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := j.f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	fn := func(id uuid.UUID, file string) {
		if err := upload(context.Background(), id, file); err != nil {
			slog.Error("failed to upload journal file", "err", err)
		} else {
			// os.Remove(file)
		}
	}
	if async {
		go fn(j.curID, j.f.Name())
	} else {
		fn(j.curID, j.f.Name())
	}

	j.f = nil
	j.w = nil
	j.n = 0
	return nil
}

func (j *Journal) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.flushResetLocked(false)
}

// advanceLocked flushes buffered bytes to the current journal file (if any)
// and advances the writer to a new file.
func (j *Journal) advanceLocked() error {
	if j.w != nil {
		if err := j.flushResetLocked(true); err != nil {
			return fmt.Errorf("flush: %w", err)
		}
	}

	j.curID = uuid.New()
	now := time.Now().UTC()
	f, err := os.CreateTemp(j.dir, fmt.Sprintf("%s_%s_*.subtjrnl", now.Format("20060102150405"), j.curID))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	j.f = f
	j.w = bufio.NewWriterSize(f, 1<<20)
	if _, err := j.w.Write(Magic[:]); err != nil {
		return fmt.Errorf("write magic: %w", err)
	}
	j.n += len(Magic)

	var uname unix.Utsname
	if err := unix.Uname(&uname); err != nil {
		return fmt.Errorf("uname: %w", err)
	}

	cstr := func(b [65]byte) string { return string(b[:bytes.IndexByte(b[:], 0)]) }
	b, err := proto.Marshal(&JournalInfo{
		CreateTime:    now.UnixNano(),
		Hostname:      cstr(uname.Nodename),
		Pid:           int64(os.Getpid()),
		GoArch:        runtime.GOARCH,
		KernelName:    cstr(uname.Sysname),
		KernelVersion: cstr(uname.Release),
		KernelArch:    cstr(uname.Machine),
	})
	if err != nil {
		return fmt.Errorf("marshal JournalInfo: %w", err)
	}
	if err := j.writePageLocked(PageType_JOURNAL_BEGIN, j.curID, b); err != nil {
		return fmt.Errorf("write JOURNAL_BEGIN: %w", err)
	}
	return nil
}

func (j *Journal) writePageLocked(t PageType, id uuid.UUID, body []byte) error {
	if _, err := j.w.Write(binary.BigEndian.AppendUint16(nil, uint16(len(body)))); err != nil {
		return fmt.Errorf("write size: %w", err)
	}
	if _, err := j.w.Write(binary.BigEndian.AppendUint16(nil, uint16(t))); err != nil {
		return fmt.Errorf("write type: %w", err)
	}
	if _, err := j.w.Write(id[:]); err != nil {
		return fmt.Errorf("write ID: %w", err)
	}
	if _, err := j.w.Write(body); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	j.n += journalPageHeaderSize + len(body)
	return nil
}

func (j *Journal) writePage(pt PageType, id uuid.UUID, raw []byte) error {
	if len(raw) >= 1<<16 {
		return fmt.Errorf("write: data size does not fit in uint16: %d > %d", len(raw), 1<<16)
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.w == nil || j.n+journalPageHeaderSize+len(raw) > journalMaxFileSize {
		if err := j.advanceLocked(); err != nil {
			return fmt.Errorf("advance: %w", err)
		}
	}
	return j.writePageLocked(pt, id, raw)
}

func (j *Journal) writeProto(pt PageType, id uuid.UUID, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	return j.writePage(pt, id, b)
}

func (j *Journal) writeCompressed(pt PageType, id uuid.UUID, raw []byte) error {
	comp := j.pool.Get().([]byte)
	defer j.pool.Put(comp)
	return j.writePage(pt, id, snappy.Encode(comp, raw))
}

type Conn struct {
	ID       uuid.UUID
	Request  io.WriteCloser
	Response io.WriteCloser

	journal  *Journal
	protocol string
}

func (j *Journal) NewConn(protocol string, tcp *TCPInfo, tls *TLSInfo) (*Conn, error) {
	c := &Conn{ID: uuid.New(), journal: j, protocol: protocol}
	switch protocol {
	case "http/1":
		c.initHTTP1()
	case "http/2":
		c.initHTTP2()
	default:
		return nil, fmt.Errorf("unknown protocol %q", protocol)
	}

	if err := j.writeProto(PageType_CONN_BEGIN, c.ID, &ConnBegin{TcpInfo: tcp, TlsInfo: tls}); err != nil {
		return nil, fmt.Errorf("write CONN_BEGIN: %w", err)
	}
	return c, nil
}

// stream is a buffered writer that represents either the request side or the
// response side of a single HTTP request.
type stream struct {
	id        uuid.UUID
	conn      *Conn
	isRequest bool
	buf       []byte
	total     int
	tail      int
}

func (c *Conn) newStream(id uuid.UUID, version HTTPVersion, isRequest bool) (*stream, error) {
	m := &HTTPBegin{
		ConnId:      c.ID[:],
		BeginTime:   time.Now().UnixNano(),
		HttpVersion: version,
	}
	if isRequest {
		slog.Debug(fmt.Sprintf("begin %s request", c.protocol), "conn", c.ID, "stream", id)
		if err := c.journal.writeProto(PageType_HTTP_BEGIN_REQUEST, id, m); err != nil {
			return nil, fmt.Errorf("write HTTP_BEGIN_REQUEST: %w", err)
		}
	} else {
		slog.Debug(fmt.Sprintf("begin %s response", c.protocol), "conn", c.ID, "stream", id)
		if err := c.journal.writeProto(PageType_HTTP_BEGIN_RESPONSE, id, m); err != nil {
			return nil, fmt.Errorf("write HTTP_BEGIN_RESPONSE: %w", err)
		}
	}

	buf := c.journal.pool.Get().([]byte)
	buf = buf[:journalMaxPageSize-journalPageHeaderSize]
	return &stream{
		id:        id,
		conn:      c,
		isRequest: isRequest,
		buf:       buf,
	}, nil
}

func (s *stream) write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if s.isRequest {
		return s.conn.journal.writeCompressed(PageType_HTTP_DATA_REQUEST, s.id, p)
	} else {
		return s.conn.journal.writeCompressed(PageType_HTTP_DATA_RESPONSE, s.id, p)
	}
}

func (s *stream) flush() error {
	err := s.write(s.buf[:s.tail])
	s.tail = 0
	return err
}

func (s *stream) end() error {
	if err := s.flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	defer s.conn.journal.pool.Put(s.buf[:cap(s.buf)])
	s.buf = nil

	m := &HTTPEnd{
		EndTime: time.Now().UnixNano(),
	}
	if s.isRequest {
		slog.Debug(fmt.Sprintf("end %s request", s.conn.protocol), "conn", s.conn.ID, "stream", s.id)
		if err := s.conn.journal.writeProto(PageType_HTTP_END_REQUEST, s.id, m); err != nil {
			return fmt.Errorf("write HTTP_END_REQUEST: %w", err)
		}
	} else {
		slog.Debug(fmt.Sprintf("end %s response", s.conn.protocol), "conn", s.conn.ID, "stream", s.id)
		if err := s.conn.journal.writeProto(PageType_HTTP_END_RESPONSE, s.id, m); err != nil {
			return fmt.Errorf("write HTTP_END_REQUEST: %w", err)
		}
	}
	return nil
}

func (s *stream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	s.total += len(p)
	if s.total > journalTruncateSize {
		return len(p), nil
	}

	done := 0
	for len(p) > 0 {
		if s.tail == len(s.buf) {
			if err := s.flush(); err != nil {
				return done, err
			}
		}

		n := len(p)
		if len(s.buf)-s.tail < n {
			n = len(s.buf) - s.tail
		}

		// If we're cleanly at a page boundary and there's more than one page left,
		// a copy + flush is wasteful when we can directly write to the journal.
		if s.tail == 0 && n < len(p) {
			if err := s.write(p[:n]); err != nil {
				return done, err
			}
			p = p[n:]
			done += n
			continue
		}

		copy(s.buf[s.tail:], p[:n])
		p = p[n:]
		s.tail += n
		done += n
	}
	return done, nil
}

type handleHTTP1 struct {
	conn      *Conn
	isRequest bool
	parser    *h1
	last      *uuid.UUID
	race      *atomic.Pointer[uuid.UUID]
	stream    *stream
}

func (c *Conn) initHTTP1() {
	var race atomic.Pointer[uuid.UUID]
	c.Request = &handleHTTP1{
		conn:      c,
		isRequest: true,
		parser:    newH1(),
		race:      &race,
	}
	c.Response = &handleHTTP1{
		conn:      c,
		isRequest: false,
		parser:    newH1(),
		race:      &race,
	}
}

func (h *handleHTTP1) Close() error {
	if h.stream != nil {
		if err := h.stream.end(); err != nil {
			return fmt.Errorf("end stream: %w", err)
		}
		h.stream = nil
	}
	return nil
}

// advance moves the currently active stream pointer to a new HTTP
// request/response with a new UUID.
func (h *handleHTTP1) advance() error {
	if h.stream != nil {
		if err := h.stream.end(); err != nil {
			return fmt.Errorf("end previous stream: %w", err)
		}
		h.stream = nil
	}

	if u := ptr(uuid.New()); h.race.CompareAndSwap(h.last, u) {
		h.last = u
	} else {
		h.last = h.race.Load()
	}

	s, err := h.conn.newStream(*h.last, HTTPVersion_HTTP_1_1, h.isRequest)
	if err != nil {
		return fmt.Errorf("new buffer: %w", err)
	}
	h.stream = s
	h.parser.reset()
	return nil
}

func (h *handleHTTP1) Write(b []byte) (int, error) {
	if h.stream == nil {
		if err := h.advance(); err != nil {
			return 0, fmt.Errorf("first advance: %w", err)
		}
	}

	for cur := 0; cur < len(b); {
		prev, write := cur, h.parser.inHeader
		cur = h.parser.consume(b, cur)
		if h.parser.lastErr != nil {
			panic(h.parser.lastErr)
		}

		if write {
			if _, err := h.stream.Write(b[prev:cur]); err != nil {
				return cur, fmt.Errorf("write buffer: %w", err)
			}
		}

		if h.parser.isComplete {
			if err := h.advance(); err != nil {
				return cur, fmt.Errorf("advance: %w", err)
			}
		}
	}
	return len(b), nil
}

type handleHTTP2 struct {
	conn      *Conn
	isRequest bool
	parser    *h2
	shared    *sync.Map
	streams   map[uint32]*stream
}

func (c *Conn) initHTTP2() {
	var shared sync.Map
	c.Request = &handleHTTP2{
		conn:      c,
		isRequest: true,
		shared:    &shared,
		parser:    newH2(true),
		streams:   make(map[uint32]*stream),
	}
	c.Response = &handleHTTP2{
		conn:      c,
		isRequest: false,
		shared:    &shared,
		parser:    newH2(false),
		streams:   make(map[uint32]*stream),
	}
}

func (h *handleHTTP2) Close() error {
	var errs []error
	for id, s := range h.streams {
		if s == nil {
			continue
		}
		if err := s.flush(); err != nil {
			errs = append(errs, fmt.Errorf("stream %d=%s: flush: %w", id, s.id, err))
		}
	}
	return errors.Join(errs...)
}

func (h *handleHTTP2) get(id uint32) (*stream, error) {
	if s, ok := h.streams[id]; ok {
		return s, nil
	}

	u := ptr(uuid.New())
	if x, loaded := h.shared.LoadOrStore(id, u); loaded {
		u = x.(*uuid.UUID)
	}

	s, err := h.conn.newStream(*u, HTTPVersion_HTTP_2, h.isRequest)
	if err != nil {
		return nil, fmt.Errorf("stream %d=%s: new stream: %w", id, *u, err)
	}

	h.streams[id] = s
	return s, nil
}

func (h *handleHTTP2) Write(b []byte) (int, error) {
	for cur := 0; cur < len(b); {
		n, id, write, prefix, closed, err := h.parser.consume(b[cur:])
		if err != nil {
			return cur, fmt.Errorf("consume: %w", err)
		}

		if id == 0 || (!write && !closed) {
			cur += n
			continue
		}

		s, err := h.get(id)
		if err != nil {
			return cur, fmt.Errorf("get stream %d: %w", id, err)
		}
		if s == nil {
			// This is possible iff our endpoint closed its side of the stream by
			// sending a RST_STREAM frame or a frame with the END_STREAM flag. In
			// this state, only PRIORITY frames are allowed to be sent and we don't
			// care about those, so we can skip this frame.
			cur += n
			continue
		}

		if write {
			if _, err := s.Write(prefix); err != nil {
				return cur, fmt.Errorf("stream %d=%s: write prefix: %w", id, s.id, err)
			}
			if _, err := s.Write(b[cur : cur+n]); err != nil {
				return cur, fmt.Errorf("stream %d=%s: write payload: %w", id, s.id, err)
			}
		}

		if closed {
			// If this side sends a RST_STREAM frame or a DATA/HEADERS frame with the
			// END_STREAM flag, it will never be written to again, so we can safely
			// close the buffer.
			if err := s.end(); err != nil {
				return cur, fmt.Errorf("stream %d=%s: end: %w", id, s.id, err)
			}
			h.streams[id] = nil
		}

		cur += n
	}
	return len(b), nil
}

func ptr[T any](x T) *T { return &x }

func upload(ctx context.Context, journalID uuid.UUID, path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat journal file size: %w", err)
	}

	begin := time.Now()
	slog.Debug("starting journal upload", "journalID", journalID, "path", path, "size", stat.Size())

	var cresp CreateUpload_Response
	if _, err := rpc.Call(ctx, &cresp, "/api/CreateUpload", &CreateUpload_Request{JournalId: journalID.String(), Size: stat.Size()}); err != nil {
		return fmt.Errorf("create upload: %w", err)
	}
	slog.Debug("created journal upload", "journalID", journalID, "uploadID", cresp.UploadId)

	if err := func() error {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer f.Close()

		req, err := http.NewRequestWithContext(ctx, "PUT", cresp.UploadUrl, bufio.NewReader(f))
		if err != nil {
			return fmt.Errorf("do upload: create request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("do upload: PUT: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return fmt.Errorf("do upload: %s", resp.Status)
		}

		slog.Debug("uploaded journal file", "journalID", journalID, "uploadID", cresp.UploadId)
		return nil
	}(); err != nil {
		return fmt.Errorf("do upload: %w", err)
	}

	var fresp FinishUpload_Response
	if _, err := rpc.Call(ctx, &fresp, "/api/FinishUpload", &FinishUpload_Request{UploadId: cresp.UploadId}); err != nil {
		return fmt.Errorf("finish upload: %w", err)
	}

	slog.Debug("finished journal upload", "journalID", journalID, "uploadID", cresp.UploadId, "took", time.Since(begin).Round(time.Microsecond))
	return nil
}
