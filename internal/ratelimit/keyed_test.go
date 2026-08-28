package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func TestKeyedLimiter_PerKeyIndependence(t *testing.T) {
	k := NewKeyedLimiter(2, 0.01, 100) // 2 tokens, refill slow
	// Alice gets 2 allows then a deny.
	assert.True(t, k.Allow("alice"))
	assert.True(t, k.Allow("alice"))
	assert.False(t, k.Allow("alice"))
	// Bob is untouched.
	assert.True(t, k.Allow("bob"))
	assert.True(t, k.Allow("bob"))
	assert.False(t, k.Allow("bob"))
}

func TestKeyedLimiter_LRUCap(t *testing.T) {
	k := NewKeyedLimiter(1, 0.001, 3)
	for _, key := range []string{"a", "b", "c"} {
		require.True(t, k.Allow(key))
	}
	assert.Equal(t, 3, k.Size())
	// Adding a fourth key evicts LRU "a".
	require.True(t, k.Allow("d"))
	assert.Equal(t, 3, k.Size())
	// "a" is fresh again — a full-capacity bucket allows a new first
	// call.
	require.True(t, k.Allow("a"))
}

func TestKeyedLimiter_ZeroMaxDefaults(t *testing.T) {
	k := NewKeyedLimiter(1, 0.001, 0)
	for i := 0; i < 100; i++ {
		require.True(t, k.Allow(fmt.Sprintf("key-%d", i)))
	}
	assert.Equal(t, 100, k.Size())
}

func TestKeyedLimiter_ConcurrentSafe(t *testing.T) {
	k := NewKeyedLimiter(10, 100, 10)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k.Allow(fmt.Sprintf("k-%d", i%5))
		}(i)
	}
	wg.Wait()
}

func TestWrap_AllowsWithinLimit(t *testing.T) {
	handled := 0
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		handled++
		return "ok", nil
	})
	limiter := NewKeyedLimiter(3, 0.001, 10)
	wrapped := Wrap(inner, limiter, "slack", "")

	for i := 0; i < 3; i++ {
		reply, err := wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u1"})
		require.NoError(t, err)
		assert.Equal(t, "ok", reply)
	}
	assert.Equal(t, 3, handled)

	// 4th message denied — inner not called.
	reply, err := wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u1"})
	require.NoError(t, err)
	assert.Equal(t, DefaultDeniedReply, reply)
	assert.Equal(t, 3, handled, "inner must not fire on deny")
}

func TestWrap_CustomDeniedReply(t *testing.T) {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
	limiter := NewKeyedLimiter(1, 0.001, 10)
	wrapped := Wrap(inner, limiter, "sms", "slow down please")
	// First message allowed.
	_, _ = wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u1"})      //nolint:errcheck // consume the free token
	reply, _ := wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u1"}) //nolint:errcheck // asserting reply only
	assert.Equal(t, "slow down please", reply)
}
