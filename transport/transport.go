package transport

import (
	"sync"
	"sync/atomic"
)

type Event struct {
}

type Sink interface {
	// Name returns the sink name to use for logging.
	Name() string

	// Handle is called for every event.
	HandleEvent(*Event)

	// Done returns a channel that's closed by the sink when it no longer wants
	// to handle events.
	Done() <-chan struct{}
}

type Bus struct {
	mu         sync.RWMutex
	sinks      []*Link
	dispatchMu sync.Mutex
}

func (b *Bus) list() []*Link {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*Link{}, b.sinks...)
}

func (b *Bus) add(l *Link) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sinks = append(b.sinks, l)
}

func (b *Bus) remove(l *Link) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := 0; i < len(b.sinks); i++ {
		if b.sinks[i] == l {
			b.sinks = append(b.sinks[:i], b.sinks[i+1:]...)
			return
		}
	}
}

func (b *Bus) Connect(s Sink, opts ...Option) *Link {
	options := new(options)
	for _, opt := range opts {
		opt(options)
	}

	if options.bufferSize == 0 {
		options.bufferSize = 256
	}
	if options.bufferSize > 0 {
		s = newBufferedSink(s, options.bufferSize)
	}

	l := new(Link)
	l.sink = s
	l.onClose = func() { b.remove(l) }
	l.closed = make(chan struct{})
	b.add(l)
	return l
}

// SendEvent sends an event to all sinks. It blocks until all sinks finish
// handling the event. It is safe to call concurrently from multiple goroutines
// if you don't care about the order in which events are serialised.
func (b *Bus) SendEvent(ev *Event) {
	b.dispatchMu.Lock()
	defer b.dispatchMu.Unlock()

	var wg sync.WaitGroup
	for _, l := range b.list() {
		wg.Add(1)
		go func(l *Link) {
			defer wg.Done()
			l.dispatch(ev)
		}(l)
	}
	wg.Wait()
}

type options struct {
	bufferSize int
}

type Option func(*options)

// WithBuffer defines the buffer size used to connect a sink to a bus. If size
// is -1, the sink is unbuffered. The default size is 256.
//
// Remember that bus event dispatch is serial so the system can only be as fast
// as its slowest sink. Buffered sinks make it possible to survive brief load
// spikes without much impact in most server workloads. Under prolonged load,
// they make an explicit tradeoff to drop events, which is usually also the
// right choice for most sinks within Subtrace. The ideal buffer size highly
// depends on the sink and workload pattern. The default is 256 because it
// feels like a pretty good fit for most use-cases within Subtrace but also
// because it's my favorite number.
//
// Unbuffered sinks are still useful sometimes (e.g. if the sink has its own
// buffer), but buffered sinks are probably what you want most of the time.
func WithBufferSize(size int) Option {
	return func(opts *options) {
		opts.bufferSize = size
	}
}

// Link is a sink attached to a bus.
type Link struct {
	sink     Sink
	onClose  func()
	inflight sync.WaitGroup
	closing  atomic.Bool
	closed   chan struct{}
}

// Close closes the link. It detaches the sink from the bus immediately, but if
// the most recent event handler is still running, it blocks until it finishes.
func (l *Link) Close() error {
	if !l.closing.CompareAndSwap(false, true) {
		<-l.closed
		return nil
	}

	l.onClose()
	l.inflight.Wait()
	close(l.closed)
	return nil
}

func (l *Link) dispatch(ev *Event) {
	l.inflight.Add(1)
	defer l.inflight.Done()
	if !l.closing.Load() {
		l.sink.HandleEvent(ev)
	}
}

type bufferedSink struct {
	sink Sink
	done chan struct{}
	buf  chan *Event
	wg   sync.WaitGroup
}

var _ Sink = &bufferedSink{}

func newBufferedSink(sink Sink, size int) *bufferedSink {
	s := &bufferedSink{
		sink: sink,
		done: make(chan struct{}),
		buf:  make(chan *Event, size),
	}

	done := s.sink.Done()
	go func() {
		close(s.done)
		<-done
		s.wg.Wait()
	}()

	go func() {
		for {
			select {
			case <-done:
				for {
					select {
					case <-s.buf:
						s.wg.Done()
					default:
						return
					}
				}
			case ev := <-s.buf:
				s.sink.HandleEvent(ev)
			}
		}
	}()
	return s
}

func (s *bufferedSink) Name() string {
	return s.sink.Name() + "[buf]"
}

func (s *bufferedSink) HandleEvent(ev *Event) {
	select {
	case <-s.done:
	default:
		s.wg.Add(1)
		select {
		case s.buf <- ev:
		default:
			s.wg.Done()
		}
	}
}

func (s *bufferedSink) Done() <-chan struct{} {
	return s.done
}
