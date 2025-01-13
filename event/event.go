package event

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	mu   sync.RWMutex
	keys []string
	vals map[string]string
	lazy sync.WaitGroup
}

func New() *Event {
	return &Event{
		keys: []string{"time", "event_id"},
		vals: map[string]string{
			"time":     time.Now().UTC().Format(time.RFC3339Nano),
			"event_id": uuid.NewString(),
		},
	}
}

func (src *Event) Copy() *Event {
	dst := New()
	dst.CopyFrom(src)
	return dst
}

// CopyFrom copies all tags from src except "time" and "event_id". If a key
// already exists in dst, it will be overwritten.
func (dst *Event) CopyFrom(src *Event) {
	if src == nil {
		return
	}

	src.mu.RLock()
	defer src.mu.RUnlock()

	dst.mu.Lock()
	defer dst.mu.Unlock()

	for _, key := range src.keys {
		switch key {
		case "time":
		case "event_id":
		default:
			dst.setLocked(key, src.vals[key])
		}
	}
}

func (ev *Event) Set(key string, val string) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.setLocked(key, val)
}

func (ev *Event) setLocked(key string, val string) {
	if ev.vals == nil {
		ev.vals = make(map[string]string)
	}

	if _, ok := ev.vals[key]; ok {
		ev.vals[key] = val
		return
	}

	ev.keys = append(ev.keys, key)
	ev.vals[key] = val
}

func (ev *Event) Get(key string) string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	val, _ := ev.vals[key]
	return val
}

func (ev *Event) NewLazy(key string) chan<- string {
	ch := make(chan string, 1)
	ev.lazy.Add(1)

	ev.mu.Lock()
	ev.keys = append(ev.keys, key)
	ev.mu.Unlock()

	go func() {
		defer ev.lazy.Done()
		ev.Set(key, <-ch)
	}()
	return ch
}

func (ev *Event) WaitLazy() {
	ev.lazy.Wait()
}

func (ev *Event) String() string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()

	var arr []string
	for _, key := range ev.keys {
		arr = append(arr, fmt.Sprintf("%s=%q", key, ev.vals[key]))
	}
	return strings.Join(arr, " ")
}
