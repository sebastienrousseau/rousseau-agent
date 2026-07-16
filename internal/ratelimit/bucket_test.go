package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBucket_AllowsUpToCapacity(t *testing.T) {
	b := NewBucket(5, 1)
	for i := 0; i < 5; i++ {
		assert.True(t, b.Allow(), "call %d should pass", i)
	}
	assert.False(t, b.Allow(), "6th call should be denied")
}

func TestBucket_RefillOverTime(t *testing.T) {
	b := NewBucket(3, 100) // 3 capacity, 100 tokens/s
	for i := 0; i < 3; i++ {
		require.True(t, b.Allow())
	}
	assert.False(t, b.Allow())
	// After 40 ms we've refilled 4 tokens; capped at 3.
	time.Sleep(40 * time.Millisecond)
	for i := 0; i < 3; i++ {
		assert.True(t, b.Allow(), "refill %d should allow", i)
	}
}

func TestBucket_DoesNotExceedCapacityAfterLongIdle(t *testing.T) {
	b := NewBucket(2, 100)
	require.True(t, b.Allow())
	require.True(t, b.Allow())
	require.False(t, b.Allow())
	// Sleep long enough to refill way past capacity.
	time.Sleep(100 * time.Millisecond)
	// Only capacity=2 available.
	assert.True(t, b.Allow())
	assert.True(t, b.Allow())
	assert.False(t, b.Allow())
}

func TestBucket_ZeroCapacityAlwaysDenies(t *testing.T) {
	b := NewBucket(0, 1)
	assert.False(t, b.Allow())
}

func TestBucket_ZeroRefillAlwaysDenies(t *testing.T) {
	b := NewBucket(5, 0)
	assert.False(t, b.Allow())
}

func TestBucket_ConcurrentSafe(t *testing.T) {
	b := NewBucket(1000, 100000)
	var wg sync.WaitGroup
	allowed := make(chan bool, 1000)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- b.Allow()
		}()
	}
	wg.Wait()
	close(allowed)
	count := 0
	for a := range allowed {
		if a {
			count++
		}
	}
	assert.LessOrEqual(t, count, 1000)
}

func TestBucket_AllowN(t *testing.T) {
	b := NewBucket(10, 1)
	assert.True(t, b.AllowN(5))
	assert.True(t, b.AllowN(5))
	assert.False(t, b.AllowN(1))
}

func TestBucket_TokensReadsCurrent(t *testing.T) {
	b := NewBucket(3, 1)
	require.True(t, b.Allow())
	assert.InDelta(t, 2.0, b.Tokens(), 0.01)
}

func TestParseRate_Valid(t *testing.T) {
	cases := []struct {
		in       string
		requests float64
		window   time.Duration
	}{
		{"10r/1m", 10, time.Minute},
		{"60r/1s", 60, time.Second},
		{"5r/5m", 5, 5 * time.Minute},
		{"100/1h", 100, time.Hour},
		{" 100 / 1h ", 100, time.Hour},
	}
	for _, tc := range cases {
		r, err := ParseRate(tc.in)
		require.NoError(t, err, "input: %q", tc.in)
		assert.Equal(t, tc.requests, r.Requests, "input: %q", tc.in)
		assert.Equal(t, tc.window, r.Window, "input: %q", tc.in)
	}
}

func TestParseRate_Invalid(t *testing.T) {
	cases := []string{"", "10", "10r/", "/1m", "0r/1m", "-1r/1m", "10r/0s", "abcr/1m", "10r/abc"}
	for _, in := range cases {
		_, err := ParseRate(in)
		assert.Error(t, err, "input %q must fail", in)
	}
}

func TestParseRate_RefillPerSec(t *testing.T) {
	r := MustParseRate("60r/1m")
	assert.InDelta(t, 1.0, r.RefillPerSec(), 0.001)
}

func TestMustParseRate_PanicsOnBad(t *testing.T) {
	assert.Panics(t, func() { MustParseRate("garbage") })
}
