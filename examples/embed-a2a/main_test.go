package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunTalksToItself(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	got := out.String()

	assert.Contains(t, got, "peer card: example-peer (v0.0.2), streaming=false")
	assert.Contains(t, got, `update: status=running message="thinking" output=""`)
	assert.Contains(t, got, `update: status=completed message="" output="echo: hello world"`)
	// The handler logs the task it received on the error stream.
	assert.Contains(t, errOut.String(), "a2a.task.received")
}

func TestRunReportsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer

	code := run(ctx, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-a2a: fetch card:")
}

func TestProbeRejectsEmptyEndpoint(t *testing.T) {
	var out bytes.Buffer

	err := probe(context.Background(), &out, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "client:")
	assert.Empty(t, out.String())
}

func TestProbeReportsUnreachablePeer(t *testing.T) {
	// A listener that is closed immediately gives us an address
	// nothing is serving, with no risk of hitting a real peer.
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()
	var out bytes.Buffer

	err := probe(context.Background(), &out, endpoint)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch card:")
}

func TestProbeReportsTaskRejection(t *testing.T) {
	// A peer that advertises a card but refuses task submission.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-capabilities", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent_id":"grumpy","name":"grumpy","version":"v1"}`))
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "at capacity", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer

	err := probe(context.Background(), &out, srv.URL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "submit:")
	// The card still printed before the submission failed.
	assert.Contains(t, out.String(), "peer card: grumpy (v1)")
}
