package observability

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartMetricsServer_NilLoggerStillReportsBindFailure proves the
// nil-logger default does not panic on the way to surfacing the error.
func TestStartMetricsServer_NilLoggerStillReportsBindFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	assert.Error(t, StartMetricsServer(ctx, "127.0.0.1:-1", nil))
}

// TestStartOTel_CancelledContextFailsExporterInit covers the exporter
// construction error path deterministically: the OTLP HTTP client
// reports the dead context out of Start.
func TestStartOTel_CancelledContextFailsExporterInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	shutdown, err := StartOTel(ctx, "http://127.0.0.1:4318", "test", nil)
	require.Error(t, err)
	assert.Nil(t, shutdown)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestStartOTel_NilLoggerWiresProvider drives the happy path with a nil
// logger against a local collector stand-in.
func TestStartOTel_NilLoggerWiresProvider(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(l) }()                          //nolint:errcheck // fixture
	defer func() { _ = srv.Shutdown(context.Background()) }() //nolint:errcheck // fixture

	shutdown, err := StartOTel(context.Background(), "http://"+l.Addr().String(), "v-test", nil)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	_, span := StartSpan(context.Background(), "nil-logger-span")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	assert.NoError(t, shutdown(ctx))
}
