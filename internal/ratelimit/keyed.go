package ratelimit

import (
	"container/list"
	"sync"
)

// KeyedLimiter is an LRU-bounded map of [Bucket] keyed by an
// arbitrary string identifier (typically a transport-specific sender
// id — WhatsApp JID, Slack user id, email address).
//
// The LRU cap prevents unbounded memory growth from adversarial
// senders churning distinct keys.
type KeyedLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*keyedEntry
	order    *list.List
	max      int
	capacity float64
	refill   float64
}

type keyedEntry struct {
	key    string
	bucket *Bucket
	elem   *list.Element
}

// NewKeyedLimiter returns a limiter that hands out buckets of the
// same capacity + refill for every key it sees. max caps how many
// distinct keys it tracks — the least-recently-used key is evicted
// when the limit is reached. Passing max ≤ 0 uses 10 000 as a
// default; that's 320 KB at ~32 bytes/entry, comfortably small.
func NewKeyedLimiter(capacity, refillPerSec float64, max int) *KeyedLimiter {
	if max <= 0 {
		max = 10000
	}
	return &KeyedLimiter{
		buckets:  make(map[string]*keyedEntry, max/2),
		order:    list.New(),
		max:      max,
		capacity: capacity,
		refill:   refillPerSec,
	}
}

// Allow consumes one token from the bucket keyed by key. Unknown
// keys allocate a fresh bucket at full capacity, so a first message
// from a new sender is always allowed.
func (k *KeyedLimiter) Allow(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	if e, ok := k.buckets[key]; ok {
		k.order.MoveToFront(e.elem)
		return e.bucket.Allow()
	}

	// New key — evict LRU if capped.
	if k.order.Len() >= k.max {
		lru := k.order.Back()
		if lru != nil {
			delete(k.buckets, lru.Value.(string))
			k.order.Remove(lru)
		}
	}
	b := NewBucket(k.capacity, k.refill)
	elem := k.order.PushFront(key)
	k.buckets[key] = &keyedEntry{key: key, bucket: b, elem: elem}
	return b.Allow()
}

// Size returns the current number of tracked keys. Test helper.
func (k *KeyedLimiter) Size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.buckets)
}
