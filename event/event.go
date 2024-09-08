package event

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type kv struct {
	key string
	val string
}

var Base = &Event{}

type Event struct {
	mu sync.RWMutex
	kv []kv
	wg sync.WaitGroup
}

func (ev *Event) Clone() *Event {
	ev.mu.RLock()
	defer ev.mu.RUnlock()

	child := &Event{}
	child.Set("time", time.Now().UTC().Format(time.RFC3339Nano))
	child.Set("event_id", uuid.NewString())
	for i := 0; i < len(ev.kv); i++ {
		switch ev.kv[i].key {
		case "time":
		case "event_id":
		default:
			child.kv = append(child.kv, ev.kv[i])
		}
	}
	return child
}

func (ev *Event) Set(key string, val string) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.kv = append(ev.kv, kv{key: key, val: val})
}

func (ev *Event) SetLazy(key string) chan<- string {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ch := make(chan string, 1)
	ev.wg.Add(1)
	go func() {
		defer ev.wg.Done()
		ev.Set(key, <-ch)
	}()
	return ch
}

func (ev *Event) AddLazy() chan<- struct{} {
	ch := make(chan struct{})
	ev.wg.Add(1)
	go func() {
		defer ev.wg.Done()
		<-ch
	}()
	return ch
}

func (ev *Event) WaitLazy() {
	ev.wg.Wait()
}

func (ev *Event) String() string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()

	var ret string
	for i := 0; i < len(ev.kv); i++ {
		if len(ret) > 0 {
			ret += " "
		}
		ret += fmt.Sprintf("%s=%q", ev.kv[i].key, ev.kv[i].val)
	}
	return ret
}
