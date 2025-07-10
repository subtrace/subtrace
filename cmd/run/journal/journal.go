package journal

import (
	"bufio"
	"io"
	"sync"
)

var Enabled bool = false

const maxLogLines = 4096

type Journal struct {
	Stdout io.Writer
	Stderr io.Writer

	mu  sync.RWMutex
	buf [maxLogLines]string
	idx uint64

	ch chan string
}

func New() *Journal {
	j := new(Journal)
	j.ch = make(chan string, 1<<16)

	prout, pwout := io.Pipe()
	prerr, pwerr := io.Pipe()
	j.Stdout = pwout
	j.Stderr = pwerr

	go j.listen()
	go j.loop(prout)
	go j.loop(prerr)

	return j
}

func (j *Journal) loop(r io.ReadCloser) {
	defer r.Close()

	s := bufio.NewScanner(r)
	for s.Scan() {
		line := s.Text()
		if len(line) > 1024 {
			line = line[:1024]
		}

		select {
		case j.ch <- line:
		default:
			// dropping data
		}
	}
}

func (j *Journal) listen() {
	for {
		j.addLine(<-j.ch)
	}
}

func (j *Journal) addLine(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.buf[j.idx%maxLogLines] = line
	j.idx++
}

func (j *Journal) CopyFrom(pos uint64) (uint64, []string) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	if pos == j.idx {
		return j.idx, nil
	}

	numLines := min(j.idx-pos, uint64(maxLogLines))
	bufEnd := j.idx % maxLogLines
	bufStart := (j.idx - numLines) % maxLogLines

	if bufStart < bufEnd {
		return j.idx - numLines, append([]string{}, j.buf[bufStart:bufEnd]...)
	} else {
		return j.idx - numLines, append(j.buf[bufStart:], j.buf[:bufEnd]...)
	}
}

func (j *Journal) GetIndex() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.idx
}
