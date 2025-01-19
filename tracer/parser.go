// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package tracer

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
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

	"github.com/andybalholm/brotli"
	"github.com/google/martian/v3/har"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"subtrace.dev/global"
	"subtrace.dev/pubsub"
)

var PayloadLimitBytes int64 = 4096 // bytes

type Parser struct {
	global  *global.Global
	eventID uuid.UUID

	wg       sync.WaitGroup
	errs     chan error
	begin    time.Time
	timings  har.Timings
	request  *har.Request
	response *har.Response
}

func NewParser(global *global.Global, eventID uuid.UUID) *Parser {
	return &Parser{
		global:  global,
		eventID: eventID,

		errs:  make(chan error, 2),
		begin: time.Now().UTC(),
	}
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
				h.Headers[i].Value = "<redacted>"
			}
		}

		start := time.Now()
		if err := <-sampler.errs; err != nil {
			p.errs <- fmt.Errorf("read request body: %w", err)
			return
		}
		p.timings.Send = time.Since(start).Milliseconds()

		text := sampler.data[:sampler.used]
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

		h.PostData = &har.PostData{
			MimeType: req.Header.Get("content-type"),
			Text:     string(text),
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
				h.Headers[i].Value = "<redacted>"
			}
		}

		start = time.Now()
		if err := <-sampler.errs; err != nil {
			p.errs <- fmt.Errorf("parse HAR response: %w", err)
			return
		}
		p.timings.Receive = time.Since(start).Milliseconds()

		text := sampler.data[:sampler.used]
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

		h.Content = &har.Content{
			Size:     sampler.used,
			MimeType: resp.Header.Get("content-type"),
			Text:     text,
			Encoding: "base64",
		}

		p.response = h
		p.errs <- nil
	}()
}

func (p *Parser) Finish() error {
	p.wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-p.errs; err != nil {
			return fmt.Errorf("parse as HAR: %w", err)
		}
	}

	entry := &har.Entry{
		ID:              p.eventID.String(),
		StartedDateTime: p.begin.UTC(),
		Time:            time.Since(p.begin).Milliseconds(),
		Request:         p.request,
		Response:        p.response,
		Timings:         &p.timings,
	}

	slog.Debug("HAR parser finished parsing request", "eventID", p.eventID, "took", time.Since(p.begin).Microseconds())

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}

	if p.global.Devtools != nil {
		go p.global.Devtools.Send(b)
	}

	if os.Getenv("SUBTRACE_TOKEN") != "" {
		var sendReflector, sendTunneler bool
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
		if sendReflector {
			if err := p.sendReflector(b); err != nil {
				slog.Error("failed to publish event to reflector", "eventID", p.eventID, "err", err)
			}
		}
		if sendTunneler {
			p.sendTunneler(b)
		}
	}
	return nil
}

func (p *Parser) sendReflector(harJSON []byte) error {
	b, err := proto.Marshal(&pubsub.Message{
		Concrete: &pubsub.Message_ConcreteV1{
			ConcreteV1: &pubsub.Message_V1{
				Underlying: &pubsub.Message_V1_Event{
					Event: &pubsub.Event{
						Concrete: &pubsub.Event_ConcreteV1{
							ConcreteV1: &pubsub.Event_V1{
								HarEntryJson: harJSON,
								Tags:         p.global.Config.Tags,
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

	select {
	case defaultPublisher.ch <- b:
		return nil
	default:
		return fmt.Errorf("publisher channel buffer full")
	}
}

func (p *Parser) sendTunneler(harJSON []byte) {
	ev := p.global.EventTemplate.Copy()
	ev.Set("event_id", p.eventID.String())
	ev.Set("http_har_entry", base64.RawStdEncoding.EncodeToString(harJSON))
	DefaultManager.Insert(ev.String())
}

type sampler struct {
	orig io.ReadCloser
	errs chan error
	used int64
	data []byte
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
