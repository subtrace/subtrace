// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package journal

import (
	"fmt"
)

var (
	errSizeUndefined        = fmt.Errorf("content-length is not set and transfer-encoding is not chunked")
	errInvalidContentLength = fmt.Errorf("invalid content-length value")
	errInvalidChunkSize     = fmt.Errorf("invalid chunk size")
	errBadChunkTerminator   = fmt.Errorf("bad chunk terminator")
)

// h1 is an allocation-free HTTP/1.1 state machine.
type h1 struct {
	isComplete     bool
	parsedBytes    int
	lastErr        error
	inHeader       bool // are we in the HTTP header (including method/status line)?
	skipHeaderLine bool // whether the current header line should should be skipped
	isChunked      bool // is the body a chunked transfer?

	// Number of bytes left in the HTTP body. Its meaning depends on the context:
	//   !isChunked && remaining == -1: still in the HTTP header
	//   !isChunked && remaining >=  0: number of bytes left in the body
	//    isChunked && remaining == -1: at the start of a new chunk
	//    isChunked && remaining >=  0: number of bytes left in this chunk
	remaining int

	// TODO: support HTTP trailers
	// TODO: support 101 Switching Protocols (bail out? what about Upgrade: h2c?)
}

func newH1() *h1 {
	h := new(h1)
	h.reset()
	return h
}

func (h *h1) reset() {
	h.isComplete = false
	h.parsedBytes = 0
	h.lastErr = nil
	h.inHeader = true
	h.skipHeaderLine = true
	h.isChunked = false
	h.remaining = -1
}

func (h *h1) consumeHeader(b []byte, start int) int {
	eol, ok := findCRLF(b, start, len(b))
	if !ok {
		// Header lines could be arbitrarily long but we only care about two
		// headers, content-length and transfer-encoding, both quite short. It's
		// wasteful to wait until a CRLF appears before advancing the parser
		// state when we don't really care about other headers.
		if h.skipHeaderLine {
			return len(b)
		} else if avail := len(b) - start; avail > 64 {
			h.skipHeaderLine = true
			return len(b)
		}
		return start
	}
	if h.skipHeaderLine {
		h.skipHeaderLine = false
		return eol + 2
	}

	if eol-start == 0 {
		h.inHeader = false
		return eol + 2
	}

	colon, ok := findByte(b, start, eol, ':')
	if !ok { // request => method/path line, response => status line
		return eol + 2
	}

	switch {
	// ref: https://datatracker.ietf.org/doc/html/rfc9112#name-content-length
	case isTrimLowerEqual(b, start, colon, "content-length"):
		n, ok := parseDec(b, colon+1, eol)
		if !ok {
			h.lastErr = errInvalidContentLength
		} else {
			h.remaining = n
		}

	// ref: https://datatracker.ietf.org/doc/html/rfc9112#name-transfer-encoding
	case isTrimLowerEqual(b, start, colon, "transfer-encoding"):
		h.isChunked = hasCommaSeparated(b, colon+1, eol, "chunked")
	}

	return eol + 2
}

func (h *h1) consumeBodyChunked(b []byte, cur int) int {
	// ref: https://datatracker.ietf.org/doc/html/rfc9112#name-chunked-transfer-coding
	switch {
	case h.remaining == -1: // start of chunk => parse and consume size and CRLF
		eol, ok := findCRLF(b, cur, len(b))
		if !ok || eol+2 >= len(b) {
			return cur
		}
		size, ok := parseHex(b, cur, eol)
		if !ok {
			h.lastErr = fmt.Errorf("parse %q: %w", b[cur:eol], errInvalidChunkSize)
			return cur
		}
		if size == 0 {
			if eol+4 <= len(b) {
				h.isComplete = true
				return eol + 4
			} else {
				return cur
			}
		}
		h.remaining = size
		return eol + 2

	case h.remaining > 0: // middle of chunk => consume as much payload as possible
		end := cur + h.remaining
		if end > len(b) {
			end = len(b)
		}
		h.remaining -= end - cur
		return end

	case h.remaining == 0: // end of chunk => consume chunk-terminating CRLF
		if len(b)-cur < 2 {
			return cur
		}
		if b[cur] == '\r' && b[cur+1] == '\n' {
			h.remaining = -1
			return cur + 2
		}
		h.lastErr = errBadChunkTerminator
		return cur
	}
	panic("unreachable")
}

func (h *h1) consume(b []byte, start int) int {
	if h.inHeader {
		return h.consumeHeader(b, start)
	}

	switch {
	case h.isChunked:
		return h.consumeBodyChunked(b, start)

	case !h.isChunked && h.remaining > 0:
		end := start + h.remaining
		if end > len(b) {
			end = len(b)
		}
		h.remaining -= end - start
		return end

	case !h.isChunked && h.remaining <= 0:
		h.remaining = -1
		h.isComplete = true
		return start
	}
	panic("reachable")
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// isTrimLowerEqual returns lower(trim(b[l:r])) == val.
func isTrimLowerEqual(b []byte, l, r int, val string) bool {
	// This is done allocation-free for performance reasons because the
	// equivalent using the bytes package (bytes.ToLower(bytes.TrimSpace(b)))
	// is nearly 3x slower.
	for ; l < r; l++ {
		if !isSpace(b[l]) {
			break
		}
	}
	for ; r > l; r-- {
		if !isSpace(b[r-1]) {
			break
		}
	}

	if r-l != len(val) {
		return false
	}

	for i := 0; i < r-l; i++ {
		c := b[i+l]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != val[i] {
			return false
		}
	}
	return true
}

// hasTrimLower returns true if any of the comma-separated values in b
// matches val case-insensitively.
func hasCommaSeparated(b []byte, start, end int, val string) bool {
	for i := start; i < end; {
		comma, ok := findByte(b, i, end, ',')
		if !ok {
			comma = end
		}
		if isTrimLowerEqual(b, i, comma, val) {
			return true
		}
		if !ok {
			break
		}
		i = comma + 1
	}
	return false
}

func findByte(b []byte, start, end int, ch byte) (int, bool) {
	for i := start; i < end; i++ {
		if b[i] == ch {
			return i, true
		}
	}
	return 0, false
}

// findCRLF returns the offset of the first CRLF in b after start.
func findCRLF(b []byte, start, end int) (int, bool) {
	for i := start; i < end; {
		cr, ok := findByte(b, i, end, '\r')
		if !ok || cr+1 >= len(b) {
			return 0, false
		}
		if b[cr+1] == '\n' {
			return cr, true
		}
		i = cr + 1
	}
	return 0, false
}

func parseDec(b []byte, start, end int) (int, bool) {
	for ; start < end; start++ {
		if !isSpace(b[start]) {
			break
		}
	}

	n := 0
	for i := start; i < end; i++ {
		c := b[i]
		n *= 10
		switch {
		case '0' <= c && c <= '9':
			n += int(c - '0')
		default:
			return 0, false
		}
	}
	return n, true
}

func parseHex(b []byte, start, end int) (int, bool) {
	for ; start < end; start++ {
		if !isSpace(b[start]) {
			break
		}
	}

	n := 0
	for i := start; i < end; i++ {
		c := b[i]
		n <<= 4
		switch {
		case '0' <= c && c <= '9':
			n += int(c - '0')
		case 'a' <= c && c <= 'f':
			n += int(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			n += int(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return n, true
}
