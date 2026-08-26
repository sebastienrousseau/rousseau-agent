package progress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drain collects everything currently buffered on a subscription.
func drain(sub *Subscription) []Event {
	var out []Event
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestBus_PublishFansOutToMatchingKeyOnly(t *testing.T) {
	b := NewBus(BusOptions{})
	a := b.Subscribe("alice")
	a2 := b.Subscribe("alice")
	z := b.Subscribe("bob")
	defer b.Close()

	b.Publish(Event{Key: "alice", Kind: KindThinking})

	assert.Len(t, drain(a), 1)
	assert.Len(t, drain(a2), 1)
	assert.Empty(t, drain(z))
}

func TestBus_PublishStampsAtAndDropsKeylessEvents(t *testing.T) {
	fixed := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	b := NewBus(BusOptions{Now: func() time.Time { return fixed }})
	sub := b.Subscribe("k")
	defer b.Close()

	b.Publish(Event{Kind: KindThinking})           // no key → dropped
	b.Publish(Event{Key: "k", Kind: KindThinking}) // stamped
	b.Publish(Event{Key: "k", At: fixed.Add(time.Minute)})

	got := drain(sub)
	require.Len(t, got, 2)
	assert.Equal(t, fixed, got[0].At)
	assert.Equal(t, fixed.Add(time.Minute), got[1].At)
}

func TestBus_FullRingEvictsOldestAndCountsDrops(t *testing.T) {
	b := NewBus(BusOptions{RingSize: 2})
	sub := b.Subscribe("k")
	defer b.Close()

	for i := 0; i < 5; i++ {
		b.Publish(Event{Key: "k", Kind: KindThinking, Detail: string(rune('a' + i))})
	}
	got := drain(sub)
	require.Len(t, got, 2)
	// Oldest evicted: the two newest survive.
	assert.Equal(t, "d", got[0].Detail)
	assert.Equal(t, "e", got[1].Detail)
	assert.Equal(t, 3, sub.Dropped())
}

func TestSubscription_OfferDropsWhenEvictionCannotFreeSpace(t *testing.T) {
	// An unbuffered ring can neither accept nor be evicted from, which
	// exercises the "gave up" branch of offer.
	b := NewBus(BusOptions{})
	sub := &Subscription{key: "k", ch: make(chan Event), bus: b}
	sub.offer(Event{Key: "k"})
	assert.Equal(t, 1, sub.Dropped())
}

func TestSubscription_OfferIsANoOpAfterClose(t *testing.T) {
	b := NewBus(BusOptions{})
	sub := b.Subscribe("k")
	sub.Close()
	assert.NotPanics(t, func() { sub.offer(Event{Key: "k"}) })
	assert.Equal(t, 0, sub.Dropped())
}

func TestSubscription_CloseIsIdempotentAndDetaches(t *testing.T) {
	b := NewBus(BusOptions{})
	keep := b.Subscribe("k")
	drop := b.Subscribe("k")
	drop.Close()
	drop.Close() // idempotent

	b.Publish(Event{Key: "k", Kind: KindThinking})
	assert.Len(t, drain(keep), 1)

	_, open := <-drop.Events()
	assert.False(t, open, "closed subscription channel must be closed")

	// Closing the last subscriber for a key removes the key entirely.
	keep.Close()
	b.mu.Lock()
	_, present := b.subs["k"]
	b.mu.Unlock()
	assert.False(t, present)
}

func TestBus_CloseIsIdempotentAndSilencesPublish(t *testing.T) {
	b := NewBus(BusOptions{})
	sub := b.Subscribe("k")
	b.Close()
	b.Close() // idempotent

	_, open := <-sub.Events()
	assert.False(t, open)

	assert.NotPanics(t, func() { b.Publish(Event{Key: "k"}) })

	// Subscribing after Close hands back an already-closed channel so
	// the caller's range loop terminates instead of hanging.
	late := b.Subscribe("k")
	_, open = <-late.Events()
	assert.False(t, open)
	assert.NotPanics(t, late.Close)
}

func TestBus_CloseWithAlreadyClosedSubscription(t *testing.T) {
	b := NewBus(BusOptions{})
	a := b.Subscribe("k")
	a.mu.Lock()
	a.closed = true
	close(a.ch)
	a.mu.Unlock()
	assert.NotPanics(t, b.Close)
}

func TestNewBus_AppliesDefaults(t *testing.T) {
	b := NewBus(BusOptions{})
	assert.Equal(t, DefaultRingSize, b.opts.RingSize)
	require.NotNil(t, b.opts.Now)
	assert.False(t, b.opts.Now().IsZero())
}
