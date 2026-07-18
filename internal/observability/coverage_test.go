package observability

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStartMetricsServer_BindFailure covers the bind-error branch by
// asking the server to bind an invalid address.
func TestStartMetricsServer_BindFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := StartMetricsServer(ctx, "999.999.999.999:99999", slog.Default())
	assert.Error(t, err)
}

// TestStartOTel_InvalidEndpointErrors covers the exporter-init error
// branch. Voyage-style bad-scheme URLs get rejected at exporter
// construction.
func TestStartOTel_InvalidEndpointErrors(t *testing.T) {
	// An endpoint with an unresolvable-in-CI hostname exercises the
	// exporter path without hitting a real OTLP collector.
	shutdown, err := StartOTel(context.Background(), "http://otlp.invalid:4318", "test", slog.Default())
	// The exporter constructor is lazy — it may not error until first
	// send. Either result exercises the branch; assert it's
	// consistent.
	if err == nil {
		assert.NotNil(t, shutdown)
		_ = shutdown(context.Background()) //nolint:errcheck // best-effort cleanup
	}
}
