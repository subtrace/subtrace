// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package journal

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestConsumeH1(t *testing.T) {
	const (
		respSimple               = "HTTP/1.1 200 OK\r\n" + "Content-Length: 5\r\n" + "\r\n" + "Hello"
		respChunked              = "HTTP/1.1 200 OK\r\n" + "Transfer-Encoding: chunked\r\n" + "\r\n" + "3\r\nabc\r\n" + "5\r\nhello\r\n" + "0\r\n\r\n"
		respPartialSimple        = "HTTP/1.1 200 OK\r\n" + "Content-Length: 20\r\n" + "\r\n" + "Hello"
		respPartialChunked       = "HTTP/1.1 200 OK\r\n" + "Transfer-Encoding: chunked\r\n" + "\r\n" + "10\r\nabc"
		respInvalidContentLength = "HTTP/1.1 200 OK\r\n" + "Content-Length: 13x4\r\n" + "\r\n" + "Hello"
		respInvalidChunkSize     = "HTTP/1.1 200 OK\r\n" + "Transfer-Encoding: chunked\r\n" + "\r\n" + "xyz\r\nabcdef\r\n" + "0\r\n\r\n"
		respSkipBody             = "HTTP/1.1 200 OK\r\n" + "\r\n" + "Hello"
		respBadChunkTerminator   = "HTTP/1.1 200 OK\r\n" + "Transfer-Encoding: chunked\r\n" + "\r\n" + "3\r\nabcdef" + "0\r\n\r\n"
	)

	tests := []struct {
		name       string
		data       string
		start      int
		result     int
		isComplete bool
		err        error
	}{
		{
			name:       "single simple",
			data:       respSimple,
			result:     len(respSimple),
			isComplete: true,
			err:        nil,
		},
		{
			name:       "single chunked",
			data:       respChunked,
			result:     len(respChunked),
			isComplete: true,
			err:        nil,
		},
		{
			name:       "multiple complete",
			data:       respSimple + respChunked,
			result:     len(respSimple) + len(respChunked),
			isComplete: true,
			err:        nil,
		},
		{
			name:       "start from middle",
			data:       respSimple + respChunked,
			start:      len(respSimple),
			result:     len(respSimple) + len(respChunked),
			isComplete: true,
			err:        nil,
		},
		{
			name:       "single partial",
			data:       respPartialSimple,
			result:     len(respPartialSimple),
			isComplete: false,
			err:        nil,
		},
		{
			name:       "multiple partial simple",
			data:       respSimple + respPartialSimple,
			result:     len(respSimple) + len(respPartialSimple),
			isComplete: false,
			err:        nil,
		},
		{
			name:       "multiple partial chunked",
			data:       respSimple + respPartialChunked,
			result:     len(respSimple) + len(respPartialChunked),
			isComplete: false,
			err:        nil,
		},
		{
			name:       "invalid content length",
			data:       respInvalidContentLength,
			result:     strings.Index(respInvalidContentLength, "\r\nHello"),
			isComplete: false,
			err:        errInvalidContentLength,
		},
		{
			name:       "invalid chunk size",
			data:       respInvalidChunkSize,
			result:     strings.Index(respInvalidChunkSize, "xyz\r\n"),
			isComplete: false,
			err:        errInvalidChunkSize,
		},
		{
			name:       "skip body",
			data:       respSkipBody,
			result:     strings.Index(respSkipBody, "Hello"),
			isComplete: true,
			err:        nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buf := []byte(test.data)
			cur := test.start
			h := newH1()
			for {
				old := cur
				cur = h.consume(buf, cur)
				if cur == old || h.lastErr != nil {
					break
				}
				if h.isComplete {
					h.reset()
				}
			}
			if !errors.Is(h.lastErr, test.err) {
				t.Errorf("got err %v, want err %v", h.lastErr, test.err)
				return
			}
			if cur != test.result {
				t.Errorf("got result %d, want result %d", cur, test.result)
				return
			}
		})
	}
}

// 2024-02-02, linux/arm64, 8 CPUs, M2 (0B, 1KB, 256KB):
// chunked: 13M rps @ 770 MB/s, 10M rps @ 4.9 GB/s, 300K rps @  31 GB/s
// simple:  14M rps @ 780 MB/s, 13M rps @ 6.3 GB/s, 3.5M rps @ 388 GB/s
func BenchmarkConsumeH1(b *testing.B) {
	for _, isChunked := range []bool{true, false} {
		for _, body := range []int{0, 16, 256, 1 << 10, 4 << 10, 16 << 10, 256 << 10} {
			b.Run(fmt.Sprintf("isChunked=%v body=%d", isChunked, body), func(b *testing.B) {
				rng := rand.New(rand.NewSource(0))
				totalSize := 32<<20 + rng.Intn(64<<20) // something large enough to not fit in the L1/L2 cache
				var buf []byte
				for len(buf) < totalSize {
					buf = append(buf, []byte("HTTP/1.1 200 OK\r\n")...)
					for h := 2 + int(rand.ExpFloat64()); h > 0; h-- {
						name := fmt.Sprintf("header-%d", h)
						value := fmt.Sprintf("%016x", rng.Uint64())
						buf = append(buf, []byte(fmt.Sprintf("%s: %s\r\n", name, value))...)
					}
					buf = append(buf, []byte("\r\n")...)

					size := 0
					if body > 0 {
						size = body - body/2 + rng.Intn(body)
					}
					data := make([]byte, size/2)
					if _, err := rng.Read(data); err != nil {
						panic(err)
					}
					data = []byte(hex.EncodeToString(data))

					buf = append(buf, []byte("GET / HTTP/1.1\r\n")...)
					if isChunked {
						buf = append(buf, []byte("Transfer-Encoding: chunked\r\n")...)
						buf = append(buf, []byte("\r\n")...)
						for len(data) > 0 {
							chunkSize := 512 + rng.Intn(512)
							if chunkSize > len(data) {
								chunkSize = len(data)
							}
							buf = append(buf, []byte(fmt.Sprintf("%x\r\n", chunkSize))...)
							buf = append(buf, data[:chunkSize]...)
							buf = append(buf, []byte("\r\n")...)
							data = data[chunkSize:]
						}
						buf = append(buf, []byte("0\r\n\r\n")...)
					} else {
						buf = append(buf, []byte(fmt.Sprintf("Content-Length: %d\r\n", size))...)
						buf = append(buf, []byte("\r\n")...)
						buf = append(buf, []byte(data)...)
					}
				}

				b.ReportAllocs()
				b.ResetTimer()
				processed := 0
				for i := 0; i < b.N; {
					h := newH1()
					start, end := 0, 4096
					for start < len(buf) {
						next := h.consume(buf[:end], start)
						if h.lastErr != nil {
							b.Fatalf("unexpected error: %v", h.lastErr)
						}
						processed += next - start
						if h.isComplete {
							h.reset()
							i++
							if i >= b.N {
								break
							}
						} else if start == next {
							end += 4096
							if end > len(buf) {
								end = len(buf)
							}
						}
						start = next
					}
				}
				b.SetBytes(int64(float64(processed) / float64(b.N)))
			})
		}
	}
}
