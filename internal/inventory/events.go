package inventory

import "sync"

// EventType names a lifecycle transition.
type EventType string

const (
	// EventDiscovered fires the first time a JVM is seen.
	EventDiscovered EventType = "discovered"
	// EventStateChanged fires for transitions with no more specific event.
	EventStateChanged EventType = "state_changed"
	// EventAttached fires when instrumentation succeeds.
	EventAttached EventType = "attached"
	// EventAttachFailed fires when an injection attempt fails.
	EventAttachFailed EventType = "attach_failed"
	// EventExited fires when a tracked process disappears.
	EventExited EventType = "exited"
)

// Event is one lifecycle notification.
type Event struct {
	Type  EventType `json:"type"`
	Entry *Entry    `json:"entry"`
}

// subscriberBuffer is how many events a slow subscriber may fall behind before
// its events start being dropped.
const subscriberBuffer = 64

// eventBus fans lifecycle events out to subscribers.
//
// Publishing never blocks: a subscriber that cannot keep up loses events rather
// than stalling the reconcile loop. The UI recovers by refetching the full list,
// so a dropped event costs a redraw, not correctness.
type eventBus struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan Event
}

func newEventBus() *eventBus {
	return &eventBus{subs: make(map[int]chan Event)}
}

// subscribe returns a channel of events and a function that closes it. The
// cancel function is safe to call more than once.
func (b *eventBus) subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan Event, subscriberBuffer)
	b.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if c, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(c)
			}
		})
	}
	return ch, cancel
}

// publish delivers an event to every subscriber with room for it.
func (b *eventBus) publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Subscriber is behind; drop rather than block the caller.
		}
	}
}
