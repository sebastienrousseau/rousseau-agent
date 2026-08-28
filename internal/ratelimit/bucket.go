// Package ratelimit implements a per-key token-bucket rate limiter
// used to protect the daemon from a single JID flooding the LLM
// budget or an outbound transport.
//
// The bucket algorithm is chosen over sliding-window for two reasons:
// constant memory per key (four floats + a timestamp), and burst-
// friendly semantics that match how a chat user actually types.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a single token bucket. All fields are private; construct
// via [NewBucket] and interact via [Bucket.Allow].
//
// A Bucket is safe for concurrent use.
type Bucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	refill   float64 // tokens per second
	last     time.Time
	nowFn    func() time.Time
}

// NewBucket returns a bucket with initial and maximum tokens equal to
// capacity, refilling at refillPerSec tokens per second. Passing
// capacity ≤ 0 or refill ≤ 0 yields a bucket that always denies —
// this makes it safe to convert a garbled config value into a
// permanently-locked limiter rather than panicking at construction.
func NewBucket(capacity, refillPerSec float64) *Bucket {
	return &Bucket{
		tokens:   capacity,
		capacity: capacity,
		refill:   refillPerSec,
		last:     time.Now(),
		nowFn:    time.Now,
	}
}

// Allow consumes one token if available. Returns true on allow,
// false on deny. Deny does not consume a token.
func (b *Bucket) Allow() bool { return b.AllowN(1) }

// AllowN consumes n tokens if the bucket has at least n. Returns
// true on allow, false on deny.
func (b *Bucket) AllowN(n float64) bool {
	if b.capacity <= 0 || b.refill <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.nowFn()
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.refill
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now

	if b.tokens >= n {
		b.tokens -= n
		return true
	}
	return false
}

// Tokens returns the current fractional token count. Primarily useful
// in tests.
func (b *Bucket) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}
