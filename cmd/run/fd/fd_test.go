// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package fd

import (
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBasic(t *testing.T) {
	b := make([]byte, 10000)
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}

	fd := NewFD(1234)
	fd.DecRef()

	var wg sync.WaitGroup

	var entries atomic.Uint64
	var done atomic.Bool
	final := -1
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer t.Logf("finished close")
		time.Sleep(30 * time.Millisecond)
		if !fd.IncRefLockForClose() {
			t.Fatalf("failed to incref for close")
		}
		defer fd.DecRef()
		final = int(entries.Load())
		done.Store(true)
		t.Logf("closing (entered=%d)", final)
	}()

	rounds := 0
	for !done.Load() {
		rounds++
		for i := 0; i < len(b); i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := fd.IncRef(); err != nil {
					return
				}
				entries.Add(1)
				defer fd.DecRef()
				dur := time.Duration(int(b[i])) * time.Microsecond
				time.Sleep(dur)
				if got := fd.FD(); got != 1234 {
					t.Fatalf("failed: got %d, want 1234", got)
				}
			}(i)
		}
	}

	wg.Wait()

	if got := int(entries.Load()); got != final {
		t.Fatalf("got %d, want %d", got, final)
	}
	t.Logf("final: %d/%d entries", entries.Load(), rounds*len(b))
}
