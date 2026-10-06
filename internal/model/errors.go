package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// ProviderError is a provider adapter's typed report of an upstream
// failure: the HTTP status the API answered with and, when the
// response carried one, how long it asked us to wait. Wrappers such as
// the retry and breaker providers classify on it instead of matching
// error text.
type ProviderError struct {
	// Status is the upstream HTTP status, 0 when the failure happened
	// before a response (dial, TLS, timeout).
	Status int
	// RetryAfter is the server's Retry-After, zero when absent.
	RetryAfter time.Duration
	// Err is the adapter's original error.
	Err error
}

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Err.Error(), e.Status)
}

// Unwrap exposes the adapter's original error to errors.Is / errors.As.
func (e *ProviderError) Unwrap() error { return e.Err }

// Retryable reports whether a fresh attempt could succeed: request
// timeouts, conflicts, rate limits and server-side failures. Client
// errors (bad request, auth, not found, payload too large) are not.
func (e *ProviderError) Retryable() bool {
	switch e.Status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	case http.StatusNotImplemented, http.StatusHTTPVersionNotSupported:
		return false
	}
	return e.Status >= 500 || e.Status == 0
}

// NewProviderError wraps err with the status and the Retry-After header
// of resp (nil-safe). Adapters call it on the SDK error they got back.
func NewProviderError(err error, status int, resp *http.Response) *ProviderError {
	pe := &ProviderError{Status: status, Err: err}
	if resp != nil {
		pe.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return pe
}

// parseRetryAfter accepts the delay-seconds form and the HTTP-date
// form of Retry-After (RFC 9110 §10.2.3); anything else is zero.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// Retryable classifies any provider error. Context errors are never
// retryable (the caller went away). A ProviderError decides for
// itself; a network timeout is retryable; everything else is not.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

// RetryAfterOf returns the server-requested delay carried by err, or
// zero.
func RetryAfterOf(err error) time.Duration {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
}
