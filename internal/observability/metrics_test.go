package observability

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func silent() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestObserveProviderLatency_recordsSample(t *testing.T) {
	ProviderLatency.Reset()
	start := time.Now().Add(-42 * time.Millisecond)
	ObserveProviderLatency("anthropic", "complete", start)
	assert.Equal(t, 1, testutil.CollectAndCount(ProviderLatency))
}

func TestObservePromptCache_splitsByTTLBucket(t *testing.T) {
	PromptCacheTokens.Reset()

	// A response with 15,664 read tokens and 8,339 creation tokens
	// (all attributed to the 1h bucket — matches the cache_creation
	// subobject Anthropic returns for a well-cached prompt).
	ObservePromptCache("anthropic", "claude-sonnet-4-6", 15664, 8339, 8339, 0)

	assert.Equal(t, 15664.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-sonnet-4-6", "read", "unspecified"),
	))
	assert.Equal(t, 8339.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-sonnet-4-6", "creation", "1h"),
	))
	// Nothing should have landed in the 5m or unspecified creation buckets.
	assert.Equal(t, 0.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-sonnet-4-6", "creation", "5m"),
	))
	assert.Equal(t, 0.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-sonnet-4-6", "creation", "unspecified"),
	))
}

func TestObservePromptCache_capturesResidualCreation(t *testing.T) {
	PromptCacheTokens.Reset()

	// Total creation = 1000, but the per-TTL split adds up to 600 —
	// the residual 400 must go into the "unspecified" TTL bucket so
	// no tokens are silently dropped.
	ObservePromptCache("anthropic", "claude-opus-4-6", 0, 1000, 400, 200)

	assert.Equal(t, 400.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-opus-4-6", "creation", "1h"),
	))
	assert.Equal(t, 200.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-opus-4-6", "creation", "5m"),
	))
	assert.Equal(t, 400.0, testutil.ToFloat64(
		PromptCacheTokens.WithLabelValues("anthropic", "claude-opus-4-6", "creation", "unspecified"),
	))
}

func TestObservePromptCache_zeroValuesEmitNothing(t *testing.T) {
	PromptCacheTokens.Reset()
	ObservePromptCache("anthropic", "test-model", 0, 0, 0, 0)
	// No labeled series should have been created — CollectAndCount reports 0.
	assert.Equal(t, 0, testutil.CollectAndCount(PromptCacheTokens))
}

func TestCounters_incrementCleanly(t *testing.T) {
	TransportIncoming.Reset()
	TransportOutgoing.Reset()
	CronFires.Reset()
	ToolCalls.Reset()
	CompressorRewrites.Reset()

	TransportIncoming.WithLabelValues("slack").Inc()
	TransportOutgoing.WithLabelValues("slack", "ok").Inc()
	CronFires.WithLabelValues("nightly", "ok").Inc()
	ToolCalls.WithLabelValues("read", "allow").Inc()
	CompressorRewrites.WithLabelValues("rewrote").Inc()

	assert.Equal(t, 1.0, testutil.ToFloat64(TransportIncoming.WithLabelValues("slack")))
	assert.Equal(t, 1.0, testutil.ToFloat64(TransportOutgoing.WithLabelValues("slack", "ok")))
	assert.Equal(t, 1.0, testutil.ToFloat64(CronFires.WithLabelValues("nightly", "ok")))
	assert.Equal(t, 1.0, testutil.ToFloat64(ToolCalls.WithLabelValues("read", "allow")))
	assert.Equal(t, 1.0, testutil.ToFloat64(CompressorRewrites.WithLabelValues("rewrote")))
}

func TestStartMetricsServer_disabledOnEmptyAddr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := StartMetricsServer(ctx, "", silent())
	assert.NoError(t, err)
}

func TestStartMetricsServer_servesRegistry(t *testing.T) {
	// Bind a random free port so parallel test runs don't collide.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- StartMetricsServer(ctx, addr, silent()) }()

	// Wait for the server to be ready.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		r, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp = r
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotNil(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	metricsResp, err := http.Get("http://" + addr + "/metrics")
	require.NoError(t, err)
	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	_ = metricsResp.Body.Close()
	assert.Contains(t, string(body), "rousseau_")
	assert.True(t, strings.Contains(string(body), "go_") || strings.Contains(string(body), "process_"),
		"expected Go runtime + process collectors to be exposed")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("metrics server did not shut down within 2s")
	}
}

func TestStartOTel_noopOnEmptyEndpoint(t *testing.T) {
	shutdown, err := StartOTel(context.Background(), "", "test", silent())
	require.NoError(t, err)
	assert.NoError(t, shutdown(context.Background()))
}

func TestStartSpan_returnsSpan(t *testing.T) {
	// With no OTel provider configured the tracer is noop; the span
	// value is still non-nil and its context stays live.
	ctx, span := StartSpan(context.Background(), "test")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

// TestStartOTel_wiresProvider drives the full wiring against a fake
// OTLP-HTTP collector: an httptest server that acknowledges every POST
// to /v1/traces with 200 OK. It exercises the exporter, resource,
// TracerProvider set-up, and the returned Shutdown path.
func TestStartOTel_wiresProvider(t *testing.T) {
	srv := http.Server{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.Handler = mux
	go func() { _ = srv.Serve(l) }()                          //nolint:errcheck // test fixture
	defer func() { _ = srv.Shutdown(context.Background()) }() //nolint:errcheck // test fixture

	endpoint := "http://" + l.Addr().String()
	shutdown, err := StartOTel(context.Background(), endpoint, "test-version", silent())
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Emit a span so the tracer provider is used, then shut down.
	_, span := StartSpan(context.Background(), "unit-test-span")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	assert.NoError(t, shutdown(ctx))
}
