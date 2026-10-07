package vertex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

func TestComplete_HTTPErrorsAreTypedForRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"quota"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{Project: "p", Region: "us-central1", Model: "m", HTTPClient: injectedClient(srv)})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), model.Request{Messages: []model.Message{model.NewUserText("hi")}})
	require.Error(t, err)

	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, http.StatusTooManyRequests, pe.Status)
	assert.Equal(t, 4*time.Second, pe.RetryAfter)
	assert.True(t, model.Retryable(err))
	assert.Contains(t, err.Error(), "vertex: HTTP 429")
}

func TestComplete_TransportErrorIsTypedWithStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	p, err := New(context.Background(), Config{Project: "p", Region: "us-central1", Model: "m", HTTPClient: injectedClient(srv)})
	require.NoError(t, err)
	srv.Close() // connection refused from here on

	_, err = p.Complete(context.Background(), model.Request{Messages: []model.Message{model.NewUserText("hi")}})
	require.Error(t, err)
	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Zero(t, pe.Status)
	assert.Contains(t, err.Error(), "vertex: post")
}
