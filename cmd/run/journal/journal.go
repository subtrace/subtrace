package journal

import (
	"bufio"
	"io"
	"sync"
)

var Enabled bool = false

const maxLogLines = 4096

type Journal struct {
	ingest struct {
		mu sync.Mutex
		pw *io.PipeWriter
	}

	store struct {
		mu  sync.RWMutex
		buf [maxLogLines]string
		idx uint64
	}
}

func New() *Journal {
	pr, pw := io.Pipe()
	j := new(Journal)
	j.ingest.pw = pw

	go j.loop(pr)

	return j
}

func (j *Journal) loop(pr *io.PipeReader) {
	defer pr.Close()

	s := bufio.NewScanner(pr)
	for s.Scan() {
		line := s.Text()
		if len(line) > 1024 {
			line = line[:1024]
		}

		j.addLine(line)
	}
}

func (j *Journal) addLine(line string) {
	j.store.mu.Lock()
	defer j.store.mu.Unlock()

	j.store.buf[j.store.idx%maxLogLines] = line
	j.store.idx++
}

func (j *Journal) CopyFrom(start uint64) []string {
	j.store.mu.RLock()
	defer j.store.mu.RUnlock()

	numLines := min(j.store.idx-start, maxLogLines)
	bufEnd := j.store.idx % maxLogLines
	bufStart := (j.store.idx - numLines) % maxLogLines

	if bufStart <= bufEnd {
		return append([]string{}, j.store.buf[bufStart:bufEnd+1]...)
	} else {
		return append(j.store.buf[bufStart:], j.store.buf[:bufEnd+1]...)
	}
}

func (j *Journal) GetIndex() uint64 {
	j.store.mu.RLock()
	defer j.store.mu.RUnlock()
	return j.store.idx
}

func (j *Journal) Write(data []byte) (int, error) {
	j.ingest.mu.Lock()
	defer j.ingest.mu.Unlock()
	return j.ingest.pw.Write(data)
}
