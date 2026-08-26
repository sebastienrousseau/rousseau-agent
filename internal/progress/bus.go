package progress

import (
	"sync"
	"time"
)

// DefaultRingSize is the per-subscriber buffer depth used when
// BusOptions.RingSize is zero.
const DefaultRingSize = 64

// BusOptions tunes a Bus.
type BusOptions struct {
	// RingSize is the per-subscriber buffer depth. A full ring drops
	// the OLDEST event and increments the subscriber's dropped
	// counter. Zero uses DefaultRingSize.
	RingSize int
	// Now is the clock used to stamp events that arrive without an At.
	// Nil uses time.Now.
	Now func() time.Time
}

// Bus fans progress events out to per-conversation subscribers.
//
// Publish never blocks and never returns an error: progress is
// best-effort telemetry, and an agent loop that stalls because a chat
// socket is slow is a far worse failure than a lossy progress feed.
// The Coalescer only ever renders the latest folded state, so dropping
// an intermediate event changes nothing the user would have seen — it
// is surfaced as a "…" marker on the next update.
//
// A Bus is safe for concurrent use.
type Bus struct {
	mu     sync.Mutex
	subs   map[string][]*Subscription
	opts   BusOptions
	closed bool
}

// NewBus constructs a Bus.
func NewBus(opts BusOptions) *Bus {
	if opts.RingSize <= 0 {
		opts.RingSize = DefaultRingSize
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Bus{subs: map[string][]*Subscription{}, opts: opts}
}

// Subscription is one consumer's view of the events for a single
// conversation key.
type Subscription struct {
	key     string
	ch      chan Event
	bus     *Bus
	mu      sync.Mutex
	dropped int
	closed  bool
}

// Events returns the channel progress events arrive on. It is closed
// when the Subscription (or the Bus) is closed.
func (s *Subscription) Events() <-chan Event { return s.ch }

// Dropped returns how many events were discarded because the consumer
// could not keep up.
func (s *Subscription) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close detaches the Subscription from its Bus and closes the event
// channel. Safe to call multiple times.
func (s *Subscription) Close() {
	s.bus.unsubscribe(s)
}

// Subscribe registers a consumer for key. The caller MUST Close the
// Subscription when it is done or the Bus retains it forever.
func (b *Bus) Subscribe(key string) *Subscription {
	sub := &Subscription{key: key, ch: make(chan Event, b.opts.RingSize), bus: b}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		sub.closed = true
		close(sub.ch)
		return sub
	}
	b.subs[key] = append(b.subs[key], sub)
	return sub
}

// Publish satisfies Publisher. Events with an empty Key are dropped —
// there is nowhere to route them.
func (b *Bus) Publish(ev Event) {
	if ev.Key == "" {
		return
	}
	if ev.At.IsZero() {
		ev.At = b.opts.Now()
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	targets := make([]*Subscription, len(b.subs[ev.Key]))
	copy(targets, b.subs[ev.Key])
	b.mu.Unlock()

	for _, sub := range targets {
		sub.offer(ev)
	}
}

// offer pushes ev into the subscription ring, evicting the oldest
// event when the ring is full.
func (s *Subscription) offer(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
		return
	default:
	}
	// Ring full: evict oldest, then retry once. A second failure means
	// a concurrent consumer refilled it — drop and move on rather than
	// spin.
	select {
	case <-s.ch:
		s.dropped++
	default:
	}
	select {
	case s.ch <- ev:
	default:
		s.dropped++
	}
}

// unsubscribe removes sub from the bus and closes its channel.
func (b *Bus) unsubscribe(sub *Subscription) {
	b.mu.Lock()
	list := b.subs[sub.key]
	kept := list[:0]
	for _, s := range list {
		if s != sub {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		delete(b.subs, sub.key)
	} else {
		b.subs[sub.key] = kept
	}
	b.mu.Unlock()

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	close(sub.ch)
}

// Close detaches every subscriber and closes their channels. Publish
// becomes a no-op afterwards. Safe to call multiple times.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	var all []*Subscription
	for _, list := range b.subs {
		all = append(all, list...)
	}
	b.subs = map[string][]*Subscription{}
	b.mu.Unlock()

	for _, sub := range all {
		sub.mu.Lock()
		if !sub.closed {
			sub.closed = true
			close(sub.ch)
		}
		sub.mu.Unlock()
	}
}

// Compile-time check.
var _ Publisher = (*Bus)(nil)
