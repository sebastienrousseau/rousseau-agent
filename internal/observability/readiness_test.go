package observability

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReady_NoCheckRegistered(t *testing.T) {
	SetReadinessCheck(nil)
	t.Cleanup(func() { SetReadinessCheck(nil) })
	assert.ErrorIs(t, Ready(), ErrNoReadinessCheck)
}

// /healthz must stay 200 while /readyz follows the transport link:
// the Helm chart points readiness at /readyz precisely so a pod with
// a dead bridge leaves the Service endpoints.
func TestMetricsServer_ReadyzFollowsRegisteredCheck(t *testing.T) {
	t.Cleanup(func() { SetReadinessCheck(nil) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = StartMetricsServer(ctx, addr, nil) }() //nolint:errcheck // exercised via HTTP below

	get := func(path string) (int, string) {
		t.Helper()
		var (
			code int
			body string
		)
		require.Eventually(t, func() bool {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
			if err != nil {
				return false
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}
			b, _ := io.ReadAll(resp.Body) //nolint:errcheck // test
			_ = resp.Body.Close()
			code, body = resp.StatusCode, string(b)
			return true
		}, 3*time.Second, 20*time.Millisecond)
		return code, body
	}

	code, body := get("/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, body, "no transport")
	code, _ = get("/healthz")
	assert.Equal(t, http.StatusOK, code, "liveness is independent of readiness")

	SetReadinessCheck(func() error { return errors.New("whatsapp transport is not connected") })
	code, body = get("/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, body, "not connected")

	SetReadinessCheck(func() error { return nil })
	code, body = get("/readyz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body)
}

func TestObserveLicense(t *testing.T) {
	exp := time.Unix(1_900_000_000, 0)
	ObserveLicense(true, exp)
	assert.Equal(t, float64(1), testutil.ToFloat64(LicenseValid))
	assert.Equal(t, float64(1_900_000_000), testutil.ToFloat64(LicenseExpiresAt))
	ObserveLicense(false, exp)
	assert.Equal(t, float64(0), testutil.ToFloat64(LicenseValid))
	assert.Equal(t, float64(0), testutil.ToFloat64(LicenseExpiresAt))
}
