package resilience

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// scripted returns the queued errors in order, then succeeds.
type scripted struct {
	errs  []error
	calls int
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Complete(context.Context, model.Request) (model.Response, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return model.Response{}, s.errs[s.calls-1]
	}
	return model.Response{StopReason: model.StopEndTurn}, nil
}

type scriptedStreamer struct{ scripted }

func (s *scriptedStreamer) Stream(ctx context.Context, req model.Request) (<-chan model.StreamEvent, <-chan model.StreamReport, error) {
	if _, err := s.Complete(ctx, req); err != nil {
		return nil, nil, err
	}
	ev := make(chan model.StreamEvent)
	close(ev)
	rep := make(chan model.StreamReport, 1)
	rep <- model.StreamReport{Response: model.Response{StopReason: model.StopEndTurn}}
	close(rep)
	return ev, rep, nil
}

func status(code int) error { return model.NewProviderError(errors.New("upstream"), code, nil) }

func retryAfter(code int, secs string) error {
	return model.NewProviderError(errors.New("upstream"), code, &http.Response{Header: http.Header{"Retry-After": []string{secs}}})
}

// noSleep records requested delays instead of waiting.
func noSleep(delays *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
}

func TestRetry_RetriesRetryableThenSucceeds(t *testing.T) {
	inner := &scripted{errs: []error{status(429), status(503)}}
	var delays []time.Duration
	p := Retry(inner, RetryConfig{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second})
	p.(*RetryProvider).sleep = noSleep(&delays)

	_, err := p.Complete(context.Background(), model.Request{})
	require.NoError(t, err)
	assert.Equal(t, 3, inner.calls)
	require.Len(t, delays, 2)
	assert.LessOrEqual(t, delays[0], 100*time.Millisecond, "attempt 1 backoff is jittered within base")
	assert.LessOrEqual(t, delays[1], 200*time.Millisecond, "attempt 2 doubles")
	assert.Positive(t, delays[0])
}

func TestRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	inner := &scripted{errs: []error{status(500), status(500), status(500), status(500)}}
	p := Retry(inner, RetryConfig{MaxAttempts: 3})
	p.(*RetryProvider).sleep = noSleep(&[]time.Duration{})
	_, err := p.Complete(context.Background(), model.Request{})
	require.Error(t, err)
	assert.Equal(t, 3, inner.calls)
	var pe *model.ProviderError
	assert.ErrorAs(t, err, &pe)
}

func TestRetry_DoesNotRetryClientErrorsOrCancel(t *testing.T) {
	for _, e := range []error{status(400), status(401), context.Canceled, errors.New("unclassified")} {
		inner := &scripted{errs: []error{e, e}}
		p := Retry(inner, RetryConfig{MaxAttempts: 3})
		p.(*RetryProvider).sleep = noSleep(&[]time.Duration{})
		_, err := p.Complete(context.Background(), model.Request{})
		require.Error(t, err)
		assert.Equal(t, 1, inner.calls, "%v is not retried", e)
	}
}

func TestRetry_HonoursRetryAfterAndRefusesOverlong(t *testing.T) {
	inner := &scripted{errs: []error{retryAfter(429, "2")}}
	var delays []time.Duration
	p := Retry(inner, RetryConfig{MaxAttempts: 3, MaxRetryAfter: 10 * time.Second})
	p.(*RetryProvider).sleep = noSleep(&delays)
	_, err := p.Complete(context.Background(), model.Request{})
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{2 * time.Second}, delays)

	inner = &scripted{errs: []error{retryAfter(429, "3600")}}
	p = Retry(inner, RetryConfig{MaxAttempts: 3, MaxRetryAfter: 10 * time.Second})
	p.(*RetryProvider).sleep = noSleep(&delays)
	_, err = p.Complete(context.Background(), model.Request{})
	require.Error(t, err, "an hour-long Retry-After is not waited out")
	assert.Equal(t, 1, inner.calls)
}

func TestRetry_StopsWhenContextEndsDuringBackoff(t *testing.T) {
	inner := &scripted{errs: []error{status(503), status(503)}}
	p := Retry(inner, RetryConfig{MaxAttempts: 3, BaseDelay: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Complete(ctx, model.Request{})
	require.Error(t, err)
	assert.Equal(t, 1, inner.calls, "the real sleeper returns on a cancelled context")
}

func TestRetry_StreamSetupIsRetriedAndStreamingIsPreserved(t *testing.T) {
	inner := &scriptedStreamer{scripted: scripted{errs: []error{status(529)}}}
	p := Retry(inner, RetryConfig{MaxAttempts: 2})
	sp, ok := p.(model.StreamingProvider)
	require.True(t, ok, "a streaming inner provider stays streaming behind the wrapper")
	p.(*retryStreamingProvider).sleep = noSleep(&[]time.Duration{})
	ev, rep, err := sp.Stream(context.Background(), model.Request{})
	require.NoError(t, err)
	assert.Equal(t, 2, inner.calls)
	for range ev {
		// drain
	}
	r := <-rep
	assert.Equal(t, model.StopEndTurn, r.Response.StopReason)

	// A non-streaming inner yields a plain provider.
	_, isStream := Retry(&scripted{}, RetryConfig{}).(model.StreamingProvider)
	assert.False(t, isStream)
	assert.Equal(t, "scripted", Retry(&scripted{}, RetryConfig{}).Name())
}

func TestRetryConfig_Defaults(t *testing.T) {
	c := RetryConfig{}.applyDefaults()
	assert.Equal(t, 3, c.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, c.BaseDelay)
	assert.Equal(t, 10*time.Second, c.MaxDelay)
	assert.Equal(t, time.Minute, c.MaxRetryAfter)
}

func TestSleepCtx(t *testing.T) {
	assert.NoError(t, sleepCtx(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
}

func TestBreaker_PreservesStreaming(t *testing.T) {
	inner := &scriptedStreamer{}
	p := Breaker(inner, BreakerConfig{})
	sp, ok := p.(model.StreamingProvider)
	require.True(t, ok)
	ev, rep, err := sp.Stream(context.Background(), model.Request{})
	require.NoError(t, err)
	for range ev {
		// drain
	}
	assert.Equal(t, model.StopEndTurn, (<-rep).Response.StopReason)

	failing := &scriptedStreamer{scripted: scripted{errs: []error{status(503), status(503), status(503)}}}
	p = Breaker(failing, BreakerConfig{MaxFailures: 2})
	sp = p.(model.StreamingProvider)
	for i := 0; i < 2; i++ {
		_, _, err := sp.Stream(context.Background(), model.Request{})
		require.Error(t, err)
	}
	_, _, err = sp.Stream(context.Background(), model.Request{})
	assert.ErrorIs(t, err, gobreaker.ErrOpenState, "third call is refused by the open breaker without reaching upstream")
	assert.Equal(t, 2, failing.calls)

	_, isStream := Breaker(&scripted{}, BreakerConfig{}).(model.StreamingProvider)
	assert.False(t, isStream)
}
