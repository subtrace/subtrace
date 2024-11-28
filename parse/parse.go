package parse

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/martian/v3/har"
	"github.com/google/uuid"
)

type Parser struct {
	EventID  uuid.UUID
	request  atomic.Pointer[http.Request]
	response atomic.Pointer[http.Response]
	started  atomic.Bool
	signal   chan struct{}
	errors   chan error
	entry    har.Entry
}

func New(ctx context.Context) *Parser {
	eventID := uuid.New()
	p := &Parser{
		EventID: eventID,
		signal:  make(chan struct{}, 2),
		errors:  make(chan error),
		entry: har.Entry{
			ID:              eventID.String(),
			StartedDateTime: time.Now().UTC(),
			Timings:         new(har.Timings),
		},
	}
	go p.start()

	return p
}

func (p *Parser) setError(err error) {
	select {
	case p.errors <- err:
	default:
	}
}

func (p *Parser) UseRequest(req *http.Request) {
	atomic.StoreInt64(&p.entry.Timings.Send, time.Since(p.entry.StartedDateTime).Milliseconds())
	if !p.request.CompareAndSwap(nil, req) {
		p.setError(fmt.Errorf("request already present"))
		return
	}

	p.signal <- struct{}{}
}

func (p *Parser) UseResponse(resp *http.Response) {
	atomic.StoreInt64(&p.entry.Timings.Wait, time.Since(p.entry.StartedDateTime).Milliseconds())
	if !p.response.CompareAndSwap(nil, resp) {
		p.setError(fmt.Errorf("request already present"))
		return
	}

	p.signal <- struct{}{}
}

func (p *Parser) start() {
	<-p.signal
	<-p.signal

	req, err := har.NewRequest(p.request.Load(), true)
	if err != nil {
		if req, err = har.NewRequest(p.request.Load(), false); err != nil {
			p.setError(fmt.Errorf("parse request: %w", err))
			return
		} else {
			// TODO: tell the user that parsing the body failed for whatever reason
		}
	}

	for i := range req.Headers {
		switch strings.ToLower(req.Headers[i].Name) {
		case "authorization", "cookie":
			req.Headers[i].Value = "<redacted>"
		}
	}

	hresp, err := har.NewResponse(p.response.Load(), true)
	if err != nil {
		if hresp, err = har.NewResponse(p.response.Load(), false); err != nil {
			p.setError(fmt.Errorf("parse response: %w", err))
			return
		}
	}

	for i := range hresp.Headers {
		switch strings.ToLower(hresp.Headers[i].Name) {
		case "set-cookie":
			hresp.Headers[i].Value = "<redacted>"
		}
	}

	p.entry.Request = req
	p.entry.Response = hresp
	p.entry.Time = time.Since(p.entry.StartedDateTime).Milliseconds()
	p.entry.Timings.Receive = time.Since(p.entry.StartedDateTime).Milliseconds()
	p.setError(nil)
}

func (p *Parser) Wait() (*har.Entry, error) {
	if err := <-p.errors; err != nil {
		return nil, err
	}
	return &p.entry, nil
}
