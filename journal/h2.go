// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package journal

import (
	"bytes"
	"fmt"

	"golang.org/x/net/http2"
)

var errInvalidPreface = fmt.Errorf("invalid PRI prefix")

var preface [24]byte

func init() {
	// ref: https://datatracker.ietf.org/doc/html/rfc9113#name-http-2-connection-preface
	const str = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	if len(str) != len(preface) {
		panic(fmt.Sprintf("preface size mismatch: len(str)=%d, len(preface)=%d", len(str), len(preface)))
	}
	copy(preface[:], str)
}

// h2 is an allocation-free HTTP/2 state machine. It will always make progress
// when parsing to ensure that there is no additional latency introduced by the
// parser when proxying bytes.
type h2 struct {
	buf     []byte
	preface bool
	frame   struct {
		valid bool
		size  int
		typ   http2.FrameType
		flags byte
		id    uint32
	}
}

// newH2 creates a h2 parser. If preface is true, the parser will drain the
// HTTP/2 preface before frame parsing.
func newH2(preface bool) *h2 {
	return &h2{buf: make([]byte, 0, 24), preface: preface}
}

func (h *h2) ensure() (parsed bool, ok bool) {
	if len(h.buf) < 9 {
		return false, false
	}
	if h.frame.valid {
		return false, true
	}

	// ref: https://datatracker.ietf.org/doc/html/rfc9113#name-frame-format
	h.frame.size = 0
	h.frame.size |= int(h.buf[0]) << 16
	h.frame.size |= int(h.buf[1]) << 8
	h.frame.size |= int(h.buf[2]) << 0

	h.frame.typ = http2.FrameType(h.buf[3])

	h.frame.flags = h.buf[4]

	h.frame.id = 0
	h.frame.id |= uint32(h.buf[5]) << 24
	h.frame.id |= uint32(h.buf[6]) << 16
	h.frame.id |= uint32(h.buf[7]) << 8
	h.frame.id |= uint32(h.buf[8]) << 0
	h.frame.id &= 0x7fffffff

	h.frame.valid = true
	return true, true
}

// consume consumes a single HTTP/2 frame and returns:
//
// * n:      advance the pointer by this many bytes
// * id:     stream ID of the parsed frame (0 = control stream)
// * write:  if true, write b[:n] to the journal
// * prefix: if write=true, write these bytes before b[:n]
// * closed: true if this is a RST_STREAM frame or has the END_STREAM flag
// * err:    any errors encountered during parsing
//
// The prefix byte slice is valid only until next consume() call and should not
// be modified by the caller.
func (h *h2) consume(b []byte) (n int, id uint32, write bool, prefix []byte, closed bool, err error) {
	if h.preface {
		for n < len(b) && len(h.buf) < len(preface) {
			h.buf = append(h.buf, b[n])
			n++
		}
		if n > 0 || len(h.buf) < len(preface) {
			return n, 0, false, nil, false, nil
		}

		if ok := bytes.Equal(h.buf, preface[:]); !ok {
			return 0, 0, false, nil, false, errInvalidPreface
		}
		h.preface = false
		h.buf = h.buf[:0]
	}

	for n < len(b) && len(h.buf) < 9 {
		h.buf = append(h.buf, b[n])
		n++
	}
	if n > 0 {
		return n, 0, false, nil, false, nil
	}

	parsed, ok := h.ensure()
	if !ok {
		return n, 0, false, nil, false, nil
	}

	if parsed {
		// If this is the first consume() call since the frame header was buffered,
		// remember to ask the caller to prefix the data (if any) with the header.
		prefix = h.buf
	}

	if h.frame.typ == http2.FrameHeaders || h.frame.typ == http2.FrameRSTStream {
		write = true
	}

	if h.frame.size <= len(b) {
		switch h.frame.typ {
		case http2.FrameData:
			closed = h.frame.flags&0x01 != 0
		case http2.FrameHeaders:
			closed = h.frame.flags&0x01 != 0
		case http2.FrameRSTStream:
			closed = true
		}
		h.buf = h.buf[:0]
		h.frame.valid = false
		return h.frame.size, h.frame.id, write, prefix, closed, nil
	}

	h.frame.size -= len(b)
	return len(b), h.frame.id, write, prefix, false, nil
}
