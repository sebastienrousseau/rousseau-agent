package model

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProviderError_RetryableByStatus(t *testing.T) {
	for status, want := range map[int]bool{
		0: true, 408: true, 409: true, 425: true, 429: true, 500: true, 502: true, 503: true, 529: true,
		400: false, 401: false, 403: false, 404: false, 413: false, 422: false, 501: false, 505: false,
	} {
		pe := NewProviderError(errors.New("x"), status, nil)
		assert.Equal(t, want, pe.Retryable(), "status %d", status)
		assert.Equal(t, want, Retryable(pe), "Retryable(status %d)", status)
	}
}

func TestProviderError_ErrorAndUnwrap(t *testing.T) {
	base := errors.New("boom")
	pe := NewProviderError(base, 503, nil)
	assert.Equal(t, "boom (HTTP 503)", pe.Error())
	assert.ErrorIs(t, pe, base)
	assert.Equal(t, "boom", NewProviderError(base, 0, nil).Error())
}

func TestProviderError_RetryAfterHeader(t *testing.T) {
	seconds := &http.Response{Header: http.Header{"Retry-After": []string{"7"}}}
	assert.Equal(t, 7*time.Second, NewProviderError(errors.New("x"), 429, seconds).RetryAfter)
	assert.Equal(t, 7*time.Second, RetryAfterOf(NewProviderError(errors.New("x"), 429, seconds)))

	date := &http.Response{Header: http.Header{"Retry-After": []string{time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)}}}
	got := NewProviderError(errors.New("x"), 429, date).RetryAfter
	assert.InDelta(t, 30*time.Second, got, float64(3*time.Second))

	past := &http.Response{Header: http.Header{"Retry-After": []string{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)}}}
	assert.Zero(t, NewProviderError(errors.New("x"), 429, past).RetryAfter)
	junk := &http.Response{Header: http.Header{"Retry-After": []string{"soon"}}}
	assert.Zero(t, NewProviderError(errors.New("x"), 429, junk).RetryAfter)
	assert.Zero(t, RetryAfterOf(errors.New("plain")))
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRetryable_ContextAndNetwork(t *testing.T) {
	assert.False(t, Retryable(nil))
	assert.False(t, Retryable(context.Canceled))
	assert.False(t, Retryable(context.DeadlineExceeded))
	assert.False(t, Retryable(errors.New("unclassified")))
	var ne net.Error = timeoutErr{}
	assert.True(t, Retryable(ne))
	assert.False(t, Retryable(NewProviderError(context.Canceled, 500, nil)), "a cancelled call is never retried, whatever the status")
}
