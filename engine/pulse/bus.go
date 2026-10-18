package pulse

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"redx/box"
	"redx/engine/link"
)

// EventKind classifies an Event.
type EventKind string

const (
	KindEmit   EventKind = "emit"   // an `emit` action; Topic is the user's topic
	KindChange EventKind = "change" // a record was written (Config.PublishChanges)
	KindTimer  EventKind = "timer"  // timer started / fired / cancelled
	KindLoop   EventKind = "loop"   // loop protection stopped a rule
	KindError  EventKind = "error"  // a rule condition or action failed
)

// Topics used for non-emit events.
const (
	TopicChange         = "change"
	TopicTimerStarted   = "pulse.timer.started"
	TopicTimerFired     = "pulse.timer.fired"
	TopicTimerCancelled = "pulse.timer.cancelled"
	TopicLoop           = "pulse.loop"
	TopicError          = "pulse.error"
)

// Event is what live clients receive.
type Event struct {
	Seq   uint64 // engine-wide, strictly increasing
	Time  time.Time
	Kind  EventKind
	Topic string
	Rule  string     // rule that produced it ("" for change events)
	Addr  link.Addr  // record the event is about
	Data  *box.Entry // snapshot of the record at that moment (may be nil)
	Err   string     // for KindError / KindLoop
	Timer string     // timer name for KindTimer
}

// Publisher receives events from the Engine. Publish must not block and
// must not call back into the Engine.
type Publisher interface{ Publish(Event) }

// Subscriber is the live-client side: a channel of events matching a topic
// pattern. Cancel stops delivery and closes the channel.
//
// Patterns: "" or "*" match everything; "prefix*" matches topics starting
// with prefix; anything else must match the topic exactly.
type Subscriber interface {
	Subscribe(pattern string, buffer int) (events <-chan Event, cancel func())
}

// Bus is an in-process Publisher + Subscriber. Delivery never blocks the
// publisher: if a subscriber's buffer is full the event is dropped for that
// subscriber and counted (see Dropped).
type Bus struct {
	mu      sync.Mutex
	nextID  int
	subs    map[int]*busSub
	dropped atomic.Uint64
}

type busSub struct {
	pattern string
	ch      chan Event
}

func NewBus() *Bus { return &Bus{subs: map[int]*busSub{}} }

func matchTopic(pattern, topic string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == topic
}

func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if !matchTopic(s.pattern, ev.Topic) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			b.dropped.Add(1)
		}
	}
}

func (b *Bus) Subscribe(pattern string, buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	s := &busSub{pattern: pattern, ch: make(chan Event, buffer)}
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = s
	b.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			close(s.ch)
			b.mu.Unlock()
		})
	}
}

// Dropped is the number of events dropped because a subscriber was slow.
func (b *Bus) Dropped() uint64 { return b.dropped.Load() }

// Subscribers is the current subscriber count.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
