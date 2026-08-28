package resilience

import (
	"context"
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
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

// BreakerProvider wraps an [agent.Provider] with a circuit breaker.
// When the breaker is Open, Complete returns [gobreaker.ErrOpenState]
// immediately without touching the wrapped provider.
type BreakerProvider struct {
	inner    agent.Provider
	breaker  *gobreaker.CircuitBreaker[agent.Response]
	resource string
}

// NewBreakerProvider constructs a breaker-wrapped provider. resource
// is used as both the gobreaker name and the metric label.
func NewBreakerProvider(inner agent.Provider, cfg BreakerConfig) *BreakerProvider {
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

	b := gobreaker.NewCircuitBreaker[agent.Response](settings)
	observability.CircuitState.WithLabelValues(resource).Set(stateFloat(gobreaker.StateClosed))

	return &BreakerProvider{inner: inner, breaker: b, resource: resource}
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
func (p *BreakerProvider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	return p.breaker.Execute(func() (agent.Response, error) {
		return p.inner.Complete(ctx, req)
	})
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
