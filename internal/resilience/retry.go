package resilience

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// RetryConfig tunes a retrying provider wrapper. Zero-value fields
// fall back to the defaults documented on each field.
type RetryConfig struct {
	// MaxAttempts is the total number of tries including the first.
	// Default 3; 1 disables retries.
	MaxAttempts int
	// BaseDelay is the first backoff; each retry doubles it, with
	// full jitter. Default 500ms.
	BaseDelay time.Duration
	// MaxDelay caps a computed backoff. A server's Retry-After is
	// honoured up to MaxRetryAfter instead. Default 10s.
	MaxDelay time.Duration
	// MaxRetryAfter caps how long a Retry-After header may make us
	// wait; longer values give up immediately. Default 60s.
	MaxRetryAfter time.Duration
}

func (c RetryConfig) applyDefaults() RetryConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = 500 * time.Millisecond
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 10 * time.Second
	}
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = 60 * time.Second
	}
	return c
}

// RetryProvider retries Complete (and the setup of Stream) on errors
// the adapter classified as retryable (model.Retryable): rate limits,
// overloaded and other 5xx answers, request timeouts. A single 429
// used to fail the user's whole turn.
type RetryProvider struct {
	inner model.Provider
	cfg   RetryConfig
	// sleep is replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// Retry wraps inner with retries. When inner implements
// model.StreamingProvider the returned provider does too, so the
// agent loop keeps streaming; otherwise it is a plain Provider.
func Retry(inner model.Provider, cfg RetryConfig) model.Provider {
	r := &RetryProvider{inner: inner, cfg: cfg.applyDefaults(), sleep: sleepCtx}
	if s, ok := inner.(model.StreamingProvider); ok {
		return &retryStreamingProvider{RetryProvider: r, streamer: s}
	}
	return r
}

// Name reports the wrapped provider's name.
func (r *RetryProvider) Name() string { return r.inner.Name() }

// Complete forwards to the inner provider, retrying retryable errors
// with exponential backoff and full jitter, honouring Retry-After.
func (r *RetryProvider) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	var last error
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		resp, err := r.inner.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		last = err
		if !r.shouldRetry(ctx, err, attempt) {
			break
		}
	}
	return model.Response{}, last
}

// shouldRetry decides whether another attempt follows err and, when
// it does, performs the wait. It returns false on the last attempt,
// on non-retryable errors, on an over-long Retry-After and when the
// context ends during the wait.
func (r *RetryProvider) shouldRetry(ctx context.Context, err error, attempt int) bool {
	if attempt >= r.cfg.MaxAttempts || !model.Retryable(err) {
		return false
	}
	delay, ok := r.delay(err, attempt)
	if !ok {
		return false
	}
	observability.ProviderRetries.WithLabelValues(r.inner.Name()).Inc()
	return r.sleep(ctx, delay) == nil
}

// delay returns the wait before the next attempt: the server's
// Retry-After when present (capped by MaxRetryAfter, or refused), else
// exponential backoff with full jitter capped by MaxDelay.
func (r *RetryProvider) delay(err error, attempt int) (time.Duration, bool) {
	if ra := model.RetryAfterOf(err); ra > 0 {
		if ra > r.cfg.MaxRetryAfter {
			return 0, false
		}
		return ra, true
	}
	backoff := r.cfg.BaseDelay << (attempt - 1)
	if backoff > r.cfg.MaxDelay || backoff <= 0 {
		backoff = r.cfg.MaxDelay
	}
	return time.Duration(rand.Int64N(int64(backoff)) + 1), true //nolint:gosec // jitter, not a secret
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryStreamingProvider retries only the establishment of a stream.
// Once events are flowing a failure is reported to the caller as is:
// replaying a half-delivered stream would duplicate text.
type retryStreamingProvider struct {
	*RetryProvider
	streamer model.StreamingProvider
}

func (r *retryStreamingProvider) Stream(ctx context.Context, req model.Request) (<-chan model.StreamEvent, <-chan model.StreamReport, error) {
	var last error
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		events, report, err := r.streamer.Stream(ctx, req)
		if err == nil {
			return events, report, nil
		}
		last = err
		if !r.shouldRetry(ctx, err, attempt) {
			break
		}
	}
	return nil, nil, last
}
