// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package tracer

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/google/martian/v3/har"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"subtrace.dev/cmd/run/journal"
	"subtrace.dev/event"
	"subtrace.dev/filter"
	"subtrace.dev/global"
	"subtrace.dev/pubsub"
	"subtrace.dev/stats"
)

var PayloadLimitBytes int64 = 4096 // bytes

var sendReflector, sendTunneler bool

func init() {
	switch strings.ToLower(os.Getenv("SUBTRACE_REFLECTOR")) {
	case "1", "t", "true", "y", "yes":
		sendReflector, sendTunneler = true, false
	case "0", "f", "false", "n", "no":
		sendReflector, sendTunneler = false, true
	case "both":
		sendReflector, sendTunneler = true, true
	default:
		sendReflector, sendTunneler = true, false
	}
}

type WebsocketMessage struct {
	Type   string  `json:"type"`
	Time   float64 `json:"time"`
	Opcode int     `json:"opcode"`
	Data   string  `json:"data"`
}

type extendedHarEntry struct {
	*har.Entry
	WebSocketMessages []*WebsocketMessage `json:"_webSocketMessages"`
}

type Parser struct {
	global *global.Global
	event  *event.Event

	wg       sync.WaitGroup
	errs     chan error
	begin    time.Time
	timings  har.Timings
	request  *har.Request
	response *har.Response

	requestTrailer  http.Header
	responseTrailer http.Header

	websocketMessages []*WebsocketMessage

	journalIdx uint64
}

func NewParser(global *global.Global, event *event.Event) *Parser {
	var journalIdx uint64
	if journal.Enabled {
		journalIdx = global.Journal.GetIndex()
	}

	return &Parser{
		global: global,
		event:  event,

		errs:  make(chan error, 2),
		begin: time.Now().UTC(),

		journalIdx: journalIdx,
	}
}

func decodeRawProto(depth int, enc map[string]any, buf []byte) error {
	if depth > 100 {
		return fmt.Errorf("recursion depth limit exceeded")
	}
	for len(buf) > 0 {
		num, typ, tlen := protowire.ConsumeTag(buf)
		if tlen < 0 {
			return fmt.Errorf("consume tag: %v", protowire.ParseError(tlen))
		}
		if tlen > len(buf) {
			return fmt.Errorf("consume num=%v, typ=%v: short buffer: %d < %d", num, typ, tlen, len(buf))
		}
		buf = buf[tlen:]

		key := fmt.Sprintf("%d", num)
		var vlen int
		switch typ {
		case protowire.VarintType:
			enc[key], vlen = protowire.ConsumeVarint(buf)
		case protowire.Fixed32Type:
			enc[key], vlen = protowire.ConsumeFixed32(buf)
		case protowire.Fixed64Type:
			enc[key], vlen = protowire.ConsumeFixed64(buf)
		case protowire.BytesType:
			var tmp []byte
			tmp, vlen = protowire.ConsumeBytes(buf)
			if vlen >= 0 {
				if _, _, size := protowire.ConsumeTag(tmp); size >= 0 {
					m := make(map[string]any)
					if err := decodeRawProto(depth+1, m, tmp); err == nil {
						enc[key] = m
						break
					}
				}
				if utf8.Valid(tmp) {
					enc[key] = string(tmp)
				} else {
					enc[key] = base64.RawStdEncoding.EncodeToString(tmp)
				}
			}
		case protowire.StartGroupType:
			enc[key], vlen = protowire.ConsumeGroup(num, buf)
		case protowire.EndGroupType:
		default:
			return fmt.Errorf("consume num=%v, typ=%v: unknown type", num, typ)
		}
		if vlen < 0 {
			return fmt.Errorf("consume num=%v, typ=%v: parse error: %w", num, typ, protowire.ParseError(vlen))
		}
		buf = buf[vlen:]
	}
	return nil
}

func jsonify(mime string, buf []byte) ([]byte, bool) {
	switch mime {
	case "application/grpc":
		switch strings.ToLower(os.Getenv("SUBTRACE_GRPC")) {
		case "1", "y", "yes", "t", "true":
			var arr []map[string]any
			for len(buf) > 0 {
				if len(buf) < 5 {
					return nil, false
				}

				comp := buf[0]
				size := binary.BigEndian.Uint32(buf[1:5])
				if len(buf) < int(size) {
					return nil, false
				}
				buf = buf[5:]

				if int(size) > len(buf) {
					return nil, false
				}
				slice := buf[:size]
				buf = buf[size:]

				if comp != 0 {
					arr = append(arr, map[string]any{"_comp": base64.RawStdEncoding.EncodeToString(slice)})
					continue
				}

				enc := make(map[string]any)
				if err := decodeRawProto(1, enc, slice); err != nil {
					return nil, false
				}
				arr = append(arr, enc)
			}
			b, err := json.Marshal(arr)
			if err != nil {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}

func (p *Parser) UseRequest(req *http.Request) {
	sampler := newSampler(req.Body)
	req.Body = sampler

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		h, err := har.NewRequest(req, false)
		if err != nil {
			p.errs <- fmt.Errorf("parse HAR request: %w", err)
			return
		}

		for i := range h.Headers {
			switch strings.ToLower(h.Headers[i].Name) {
			case "authorization", "cookie":
				h.Headers[i].Value = p.global.Config.SantizeCredential(h.Headers[i].Value)
			}
		}

		for i := range h.Cookies {
			h.Cookies[i].Value = p.global.Config.SantizeCredential(h.Cookies[i].Value)
		}

		start := time.Now()
		if err := <-sampler.errs; err != nil {
			p.errs <- fmt.Errorf("read request body: %w", err)
			return
		}
		p.timings.Send = time.Since(start).Milliseconds()

		text := sampler.data[:sampler.used]
		if !sampler.over {
			switch req.Header.Get("content-encoding") {
			case "gzip":
				gr, err := gzip.NewReader(bytes.NewBuffer(text))
				if err != nil {
					p.errs <- fmt.Errorf("create gzip reader: %w", err)
					return
				}
				if raw, err := io.ReadAll(gr); err != nil {
					p.errs <- fmt.Errorf("read gzip: %w", err)
					return
				} else {
					text = raw
				}
			case "br":
				if raw, err := io.ReadAll(brotli.NewReader(bytes.NewBuffer(text))); err != nil {
					p.errs <- fmt.Errorf("decode brotli: %w", err)
					return
				} else {
					text = raw
				}
			}
		}

		h.PostData = &har.PostData{
			MimeType: req.Header.Get("content-type"),
			Text:     string(text),
		}

		for _, hdr := range h.Headers {
			switch strings.ToLower(hdr.Name) {
			case "content-type":
				json, ok := jsonify(hdr.Value, text)
				if ok {
					h.PostData.MimeType = "application/json"
					h.PostData.Text = string(json)
				}
			}
		}

		p.request = h
		p.errs <- nil
	}()
}

func (p *Parser) UseResponse(resp *http.Response) {
	sampler := newSampler(resp.Body)
	resp.Body = sampler

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		start := time.Now()

		h, err := har.NewResponse(resp, false)
		if err != nil {
			p.errs <- fmt.Errorf("parse HAR response: %w", err)
			return
		}

		// TODO: does the "wait" timer start before or after the request is fully
		// sent (including body)?
		p.timings.Wait = time.Since(start).Milliseconds()

		for i := range h.Headers {
			switch strings.ToLower(h.Headers[i].Name) {
			case "set-cookie":
				h.Headers[i].Value = p.global.Config.SantizeCredential(h.Headers[i].Value)
			}
		}

		for i := range h.Cookies {
			h.Cookies[i].Value = p.global.Config.SantizeCredential(h.Cookies[i].Value)
		}

		start = time.Now()
		if err := <-sampler.errs; err != nil {
			p.errs <- fmt.Errorf("parse HAR response: %w", err)
			return
		}
		p.timings.Receive = time.Since(start).Milliseconds()

		text := sampler.data[:sampler.used]
		if !sampler.over {
			switch resp.Header.Get("content-encoding") {
			case "gzip":
				gr, err := gzip.NewReader(bytes.NewBuffer(text))
				if err != nil {
					p.errs <- fmt.Errorf("create gzip reader: %w", err)
					return
				}
				if raw, err := io.ReadAll(gr); err != nil {
					p.errs <- fmt.Errorf("read gzip: %w", err)
					return
				} else {
					text = raw
				}
			case "br":
				if raw, err := io.ReadAll(brotli.NewReader(bytes.NewBuffer(text))); err != nil {
					p.errs <- fmt.Errorf("decode brotli: %w", err)
					return
				} else {
					text = raw
				}
			}
		}

		h.Content = &har.Content{
			Size:     sampler.used,
			MimeType: resp.Header.Get("content-type"),
			Text:     text,
			Encoding: "base64",
		}

		for _, hdr := range h.Headers {
			switch strings.ToLower(hdr.Name) {
			case "content-type":
				json, ok := jsonify(hdr.Value, text)
				if ok {
					h.Content.MimeType = "application/json"
					h.Content.Text = json
				}
			}
		}

		p.response = h
		p.errs <- nil
	}()
}

func (p *Parser) UseWebsocketMessages(msgs []*WebsocketMessage) {
	p.websocketMessages = msgs
}

func (p *Parser) SetRequestTrailer(tr http.Header) {
	p.requestTrailer = tr
}

func (p *Parser) SetResponseTrailer(tr http.Header) {
	p.responseTrailer = tr
}

func (p *Parser) Finish() error {
	if sendReflector {
		DefaultPublisher.inflight.Add(1)
		defer DefaultPublisher.inflight.Done()
	}

	p.wg.Wait()
	if err := errors.Join(<-p.errs, <-p.errs); err != nil {
		return err
	}

	var logidx uint64
	var loglines []string
	if journal.Enabled {
		logidx, loglines = p.global.Journal.CopyFrom(p.journalIdx)
	}

	entry := &extendedHarEntry{
		Entry: &har.Entry{
			ID:              p.event.Get("event_id"),
			StartedDateTime: p.begin.UTC(),
			Time:            time.Since(p.begin).Milliseconds(),
			Request:         p.request,
			Response:        p.response,
			Timings:         &p.timings,
		},
		WebSocketMessages: p.websocketMessages,
	}

	for k, v := range stats.Load() {
		p.event.Set(k, v)
	}

	// The event template may have changed since the process started because we add some
	// tags asynchronously. Grab an updated copy of the template to account for these new
	// tags.
	tmpl := p.global.Config.GetEventTemplate()
	tmpl.CopyFrom(p.event)
	tags := tmpl.Map()

	{
		begin := time.Now()
		match, err := p.global.Config.GetMatchingFilter(tags, entry.Entry)
		slog.Debug("evaluated filters", "eventID", p.event.Get("event_id"), "match", match, "err", err, "took", time.Since(begin).Round(time.Nanosecond))
		switch {
		case err != nil:
			fallthrough // fallthrough to ActionInclude if filter eval fails
		case match == nil:
			break
		case match.Action == filter.ActionInclude:
			break
		case match.Action == filter.ActionExclude:
			return nil
		default:
			panic(fmt.Errorf("unknown filter action %q", match.Action))
		}
	}

	// HAR v1.2 doesn't support trailers so for now we just set the counts as a
	// way to indicate to the user that there were trailers.
	if p.requestTrailer != nil {
		tags["request_trailer_count"] = fmt.Sprintf("%d", len(p.requestTrailer))
	}
	if p.responseTrailer != nil {
		tags["response_trailer_count"] = fmt.Sprintf("%d", len(p.responseTrailer))
	}

	json, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}

	if DefaultManager.log.Load() {
		method := entry.Request.Method
		if len(method) > 3 {
			method = method[:3]
		}
		fmt.Fprintf(os.Stderr, "%s  |  %d %3s %q\n", time.Now().UTC().Format("2006-01-02 15:04:05.999 UTC"), entry.Response.Status, method, entry.Request.URL)
	}

	if p.global.Devtools != nil && p.global.Devtools.HijackPath != "" {
		go p.global.Devtools.Send(json)
		return nil
	}

	if sendReflector {
		if err := p.sendReflector(tags, json, logidx, loglines); err != nil {
			slog.Error("failed to publish event to reflector", "eventID", p.event.Get("event_id"), "err", err)
		}
	}
	if sendTunneler {
		ev := p.event.Copy()
		ev.Set("http_har_entry", base64.RawStdEncoding.EncodeToString(json))
		ev.Set("har_time", fmt.Sprintf("%d", entry.Time))
		ev.Set("har_request_method", entry.Request.Method)
		ev.Set("har_request_url", entry.Request.URL)
		ev.Set("har_response_status", fmt.Sprintf("%d", entry.Response.Status))
		DefaultManager.Insert(ev.String())
	}
	return nil
}

func (p *Parser) sendReflector(tags map[string]string, json []byte, logidx uint64, loglines []string) error {
	b, err := proto.Marshal(&pubsub.Message{
		Concrete: &pubsub.Message_ConcreteV1{
			ConcreteV1: &pubsub.Message_V1{
				Underlying: &pubsub.Message_V1_Event{
					Event: &pubsub.Event{
						Concrete: &pubsub.Event_ConcreteV1{
							ConcreteV1: &pubsub.Event_V1{
								Tags:         tags,
								HarEntryJson: json,
								Log: &pubsub.Event_Log{
									Lines: loglines,
									Index: logidx + 1,
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal proto: %w", err)
	}
	return DefaultPublisher.queueWrite(b)
}

type sampler struct {
	orig io.ReadCloser
	errs chan error
	used int64
	data []byte
	over bool
}

func newSampler(orig io.ReadCloser) *sampler {
	return &sampler{
		orig: orig,
		errs: make(chan error, 1),
		data: make([]byte, PayloadLimitBytes),
	}
}

func (s *sampler) setError(err error) {
	if errors.Is(err, io.EOF) {
		err = nil
	}

	select {
	case s.errs <- err:
	default:
	}
}

func (s *sampler) Read(b []byte) (int, error) {
	n, err := s.orig.Read(b)
	if err != nil {
		s.setError(err)
	}

	if n > 0 && s.used < PayloadLimitBytes {
		c := int64(n)
		if s.used+c > PayloadLimitBytes {
			s.over = true
			c = PayloadLimitBytes - s.used
		}
		s.used += int64(copy(s.data[s.used:s.used+c], b[0:c]))
	}
	return n, err
}

func (s *sampler) Close() error {
	err := s.orig.Close()
	s.setError(err)
	return err
}
