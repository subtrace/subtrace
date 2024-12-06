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
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/martian/v3/har"
	"github.com/google/uuid"
	"subtrace.dev/event"
)

var PayloadLimitBytes int64 = 4096 // bytes

type Parser struct {
	eventID  uuid.UUID
	wg       sync.WaitGroup
	errs     chan error
	begin    time.Time
	timings  har.Timings
	request  *har.Request
	response *har.Response
}

func NewParser(eventID uuid.UUID) *Parser {
	return &Parser{
		eventID: eventID,
		errs:    make(chan error, 2),
		begin:   time.Now().UTC(),
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
		StartedDateTime: p.begin.UTC(),
		Time:            time.Since(p.begin).Milliseconds(),
		Request:         p.request,
		Response:        p.response,
		Timings:         &p.timings,
	}

	slog.Debug("HAR parser finished parsing request", "eventID", p.eventID, "took", time.Since(p.begin).Microseconds())

	b := new(bytes.Buffer)
	w := base64.NewEncoder(base64.RawStdEncoding, b)
	if err := json.NewEncoder(w).Encode(entry); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close json: %w", err)
	}

	ev := event.NewFromTemplate(event.Base)
	ev.Set("event_id", p.eventID.String())
	ev.Set("http_har_entry", b.String())
	DefaultManager.Insert(ev.String())
	return nil
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

	c := int64(n)
	if remain := PayloadLimitBytes - s.used; c > remain {
		c = remain
	}
	if c > 0 {
		copy(s.data[s.used:s.used+c], b[0:c])
		s.used += c
	}

	return n, err
}

func (s *sampler) Close() error {
	err := s.orig.Close()
	s.setError(err)
	return err
}
