package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// fakeProvider is a controllable Provider used to drive the breaker's
// state machine deterministically.
type fakeProvider struct {
	name     string
	calls    atomic.Int64
	failNext atomic.Int32 // countdown of failures to return
	err      error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Complete(ctx context.Context, _ agent.Request) (agent.Response, error) {
	f.calls.Add(1)
	if f.failNext.Load() > 0 {
		f.failNext.Add(-1)
		if f.err != nil {
			return agent.Response{}, f.err
		}
		return agent.Response{}, errors.New("boom")
	}
	return agent.Response{}, nil
}

func TestBreaker_ClosedOnSuccess(t *testing.T) {
	fp := &fakeProvider{name: "fp"}
	b := NewBreakerProvider(fp, BreakerConfig{})
	for i := 0; i < 10; i++ {
		_, err := b.Complete(context.Background(), agent.Request{})
		assert.NoError(t, err)
	}
	assert.Equal(t, int64(10), fp.calls.Load())
}

func TestBreaker_TripsAfterConsecutiveFailures(t *testing.T) {
	fp := &fakeProvider{name: "fp"}
	fp.failNext.Store(10)
	b := NewBreakerProvider(fp, BreakerConfig{MaxFailures: 3, Timeout: 30 * time.Millisecond})

	// First 3 calls hit the provider, then the breaker trips.
	for i := 0; i < 5; i++ {
		_, _ = b.Complete(context.Background(), agent.Request{}) //nolint:errcheck // exercising failure count
	}
	// After tripping, further calls return ErrOpenState without
	// touching the provider.
	before := fp.calls.Load()
	_, err := b.Complete(context.Background(), agent.Request{})
	assert.ErrorIs(t, err, gobreaker.ErrOpenState)
	assert.Equal(t, before, fp.calls.Load(), "provider must not be called while Open")
}

func TestBreaker_HalfOpenTransitionsToClosedOnSuccess(t *testing.T) {
	fp := &fakeProvider{name: "fp"}
	fp.failNext.Store(3)
	b := NewBreakerProvider(fp, BreakerConfig{MaxFailures: 3, Timeout: 20 * time.Millisecond, HalfOpenMax: 1})

	// Trip the breaker.
	for i := 0; i < 3; i++ {
		_, _ = b.Complete(context.Background(), agent.Request{}) //nolint:errcheck // exercising breaker states
	}
	// Wait past Timeout so we enter HalfOpen on the next call.
	time.Sleep(30 * time.Millisecond)

	// The provider is now healthy (failNext is 0) — the probe succeeds
	// and the breaker returns to Closed.
	_, err := b.Complete(context.Background(), agent.Request{})
	require.NoError(t, err)

	// Follow-up calls stay in Closed.
	_, err = b.Complete(context.Background(), agent.Request{})
	assert.NoError(t, err)
}

func TestBreaker_NonRetryableDoesNotTrip(t *testing.T) {
	fp := &fakeProvider{name: "fp"}
	fp.err = NonRetryable(errors.New("auth failed"))
	fp.failNext.Store(20)
	b := NewBreakerProvider(fp, BreakerConfig{MaxFailures: 3})

	// Drive 10 non-retryable failures — the breaker must stay Closed
	// (each attempt hits the provider).
	for i := 0; i < 10; i++ {
		_, err := b.Complete(context.Background(), agent.Request{})
		require.Error(t, err)
	}
	assert.Equal(t, int64(10), fp.calls.Load(), "non-retryable errors must not open the breaker")
}

func TestBreaker_ContextCancelDoesNotTrip(t *testing.T) {
	fp := &fakeProvider{name: "fp"}
	fp.err = context.Canceled
	fp.failNext.Store(20)
	b := NewBreakerProvider(fp, BreakerConfig{MaxFailures: 3})

	for i := 0; i < 10; i++ {
		_, _ = b.Complete(context.Background(), agent.Request{}) //nolint:errcheck // exercising breaker states
	}
	assert.Equal(t, int64(10), fp.calls.Load(), "ctx errors must not open the breaker")
}

func TestBreaker_NameDelegates(t *testing.T) {
	fp := &fakeProvider{name: "under"}
	b := NewBreakerProvider(fp, BreakerConfig{})
	assert.Equal(t, "under", b.Name())
}

func TestBreaker_ConfigDefaults(t *testing.T) {
	c := BreakerConfig{}.applyDefaults()
	assert.EqualValues(t, 5, c.MaxFailures)
	assert.Equal(t, 60*time.Second, c.Interval)
	assert.Equal(t, 30*time.Second, c.Timeout)
	assert.EqualValues(t, 1, c.HalfOpenMax)
}

func TestNonRetryable_NilIsNil(t *testing.T) {
	assert.NoError(t, NonRetryable(nil))
}

func TestNonRetryable_Unwrap(t *testing.T) {
	base := errors.New("underlying")
	wrapped := NonRetryable(base)
	assert.ErrorIs(t, wrapped, base)
	assert.Equal(t, "underlying", wrapped.Error())
}

func TestStateFloat_AllStates(t *testing.T) {
	// The Prometheus gauge mapping is a stable ABI — bumping any of
	// these values changes what an existing dashboard displays.
	assert.Equal(t, float64(0), stateFloat(gobreaker.StateClosed))
	assert.Equal(t, float64(1), stateFloat(gobreaker.StateHalfOpen))
	assert.Equal(t, float64(2), stateFloat(gobreaker.StateOpen))
	// Unknown states (future gobreaker additions) fall back to
	// "closed = 0" rather than panicking or exporting NaN.
	assert.Equal(t, float64(0), stateFloat(gobreaker.State(99)))
}

func TestIsNonRetryable_ContextCancelBypassesBreaker(t *testing.T) {
	// Two ways to surface a caller-side abort: context.Canceled and
	// context.DeadlineExceeded. Neither should count as a provider
	// failure — the breaker would otherwise open on a slow client
	// rather than a misbehaving upstream.
	assert.True(t, isNonRetryable(context.Canceled))
	assert.True(t, isNonRetryable(context.DeadlineExceeded))
	assert.True(t, isNonRetryable(fmt.Errorf("wrap: %w", context.Canceled)))
	assert.False(t, isNonRetryable(errors.New("provider blew up")))
	assert.False(t, isNonRetryable(nil))
}
