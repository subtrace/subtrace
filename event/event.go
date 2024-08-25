package event

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type kv struct {
	key string
	val string
}

var hostname = sync.OnceValue(func() string {
	name, _ := os.Hostname()
	return name
})

type Event struct {
	mu sync.Mutex
	kv []kv
	wg sync.WaitGroup
}

func New() *Event {
	ev := &Event{}
	ev.Set("time", time.Now().UTC().Format(time.RFC3339Nano))
	ev.Set("event_id", uuid.NewString())
	ev.Set("hostname", hostname())
	return ev
}

func (ev *Event) Clone() *Event {
	ev.mu.Lock()
	defer ev.mu.Unlock()

	other := &Event{}
	for i := 0; i < len(ev.kv); i++ {
		switch ev.kv[i].key {
		case "event_id":
			other.kv = append(other.kv, kv{key: ev.kv[i].key, val: uuid.NewString()})
		default:
			other.kv = append(other.kv, ev.kv[i])
		}
	}
	return other
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
	ev.mu.Lock()
	defer ev.mu.Unlock()

	var ret string
	for i := 0; i < len(ev.kv); i++ {
		if len(ret) > 0 {
			ret += " "
		}
		ret += fmt.Sprintf("%s=", ev.kv[i].key)
		if quoted := fmt.Sprintf("%q", ev.kv[i].val); len(quoted) > len(ev.kv[i].val)+2 {
			ret += quoted
		} else {
			ret += ev.kv[i].val
		}
	}
	return ret
}
