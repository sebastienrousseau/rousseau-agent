package resilience

import (
	"context"
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// BreakerConfig tunes a circuit breaker. Zero-value fields fall back
// to the defaults documented on each field.
type BreakerConfig struct {
	// MaxFailures is the consecutive-failure threshold before the
	// breaker trips. Default 5.
	MaxFailures uint32
	// Interval resets the failure counter after this idle window.
	// Default 60s.
	Interval time.Duration
	// Timeout is how long the breaker stays Open before entering
	// HalfOpen. Default 30s.
	Timeout time.Duration
	// HalfOpenMax is how many probe requests are allowed while the
	// breaker is HalfOpen. Default 1.
	HalfOpenMax uint32
}

func (c BreakerConfig) applyDefaults() BreakerConfig {
	if c.MaxFailures == 0 {
		c.MaxFailures = 5
	}
	if c.Interval == 0 {
		c.Interval = 60 * time.Second
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.HalfOpenMax == 0 {
		c.HalfOpenMax = 1
	}
	return c
}

// BreakerProvider wraps an [model.Provider] with a circuit breaker.
// When the breaker is Open, Complete returns [gobreaker.ErrOpenState]
// immediately without touching the wrapped provider.
type BreakerProvider struct {
	inner model.Provider
	// breaker is untyped so Complete and Stream share one failure
	// tally: an upstream that refuses streams is as down as one that
	// refuses completions.
	breaker  *gobreaker.CircuitBreaker[any]
	resource string
}

// NewBreakerProvider constructs a breaker-wrapped provider. resource
// is used as both the gobreaker name and the metric label.
func NewBreakerProvider(inner model.Provider, cfg BreakerConfig) *BreakerProvider {
	cfg = cfg.applyDefaults()
	resource := inner.Name()

	settings := gobreaker.Settings{
		Name:        resource,
		MaxRequests: cfg.HalfOpenMax,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= cfg.MaxFailures
		},
		OnStateChange: func(_ string, _, to gobreaker.State) {
			observability.CircuitState.WithLabelValues(resource).Set(stateFloat(to))
			if to == gobreaker.StateOpen {
				observability.CircuitTrips.WithLabelValues(resource).Inc()
			}
		},
		IsSuccessful: func(err error) bool {
			// From the breaker's health perspective, non-retryable
			// errors and ctx errors are "success" — they're not the
			// upstream misbehaving.
			return err == nil || isNonRetryable(err)
		},
	}

	b := gobreaker.NewCircuitBreaker[any](settings)
	observability.CircuitState.WithLabelValues(resource).Set(stateFloat(gobreaker.StateClosed))

	return &BreakerProvider{inner: inner, breaker: b, resource: resource}
}

// Breaker wraps inner with a circuit breaker. When inner implements
// model.StreamingProvider the returned provider does too, so wrapping
// no longer silently downgrades the agent loop to non-streaming.
func Breaker(inner model.Provider, cfg BreakerConfig) model.Provider {
	b := NewBreakerProvider(inner, cfg)
	if s, ok := inner.(model.StreamingProvider); ok {
		return &breakerStreamingProvider{BreakerProvider: b, streamer: s}
	}
	return b
}

// breakerStreamingProvider runs Stream's establishment through the
// breaker; a stream that fails after it was established reports its
// error to the caller without counting against the breaker, since the
// upstream did answer.
type breakerStreamingProvider struct {
	*BreakerProvider
	streamer model.StreamingProvider
}

type streamHandles struct {
	events <-chan model.StreamEvent
	report <-chan model.StreamReport
}

func (p *breakerStreamingProvider) Stream(ctx context.Context, req model.Request) (<-chan model.StreamEvent, <-chan model.StreamReport, error) {
	v, err := p.breaker.Execute(func() (any, error) {
		ev, rep, err := p.streamer.Stream(ctx, req)
		return streamHandles{events: ev, report: rep}, err
	})
	if err != nil {
		return nil, nil, err
	}
	h := v.(streamHandles) //nolint:forcetypeassert // Execute returns what the closure returned
	return h.events, h.report, nil
}

// Name reports the wrapped provider's name — the wrapper is
// transparent from the caller's perspective.
func (p *BreakerProvider) Name() string { return p.inner.Name() }

// Complete forwards to the inner provider through the breaker.
// Errors from the inner provider count against the breaker's
// consecutive-failure tally unless they are context errors or have
// been wrapped in [NonRetryable]. In either case the error is still
// surfaced to the caller — only the breaker's health metric is
// spared.
func (p *BreakerProvider) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	v, err := p.breaker.Execute(func() (any, error) {
		return p.inner.Complete(ctx, req)
	})
	if err != nil {
		return model.Response{}, err
	}
	return v.(model.Response), nil //nolint:forcetypeassert // Execute returns what the closure returned
}

// stateFloat maps a gobreaker state to the value the prometheus
// gauge exports.
func stateFloat(s gobreaker.State) float64 {
	switch s {
	case gobreaker.StateClosed:
		return 0
	case gobreaker.StateHalfOpen:
		return 1
	case gobreaker.StateOpen:
		return 2
	}
	return 0
}

// nonRetryableError marks an error that should surface to the caller
// but not trip the breaker (e.g. context cancel, auth failures the
// operator must fix before retrying is useful).
type nonRetryableError struct{ err error }

func (e *nonRetryableError) Error() string { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error { return e.err }

// NonRetryable marks err so [BreakerProvider.Complete] returns it
// to the caller without counting it as a breaker failure. Passing a
// nil error returns nil.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &nonRetryableError{err: err}
}

func isNonRetryable(err error) bool {
	if err == nil {
		return false
	}
	var nre *nonRetryableError
	if errors.As(err, &nre) {
		return true
	}
	// Context errors never trip the breaker — they're the caller's
	// signal, not the provider's fault.
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
