package license

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// L-25: a licence valid at boot must stop unlocking features once its
// exp passes, without a restart, and say so once.

func TestChecker_ExpiryIsReCheckedAtRuntime(t *testing.T) {
	pub, priv := newTestKeypair(t)
	exp := time.Now().Add(time.Hour)
	tok := signTestToken(t, priv, TierEnterprise, exp)
	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })
	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)

	var logs bytes.Buffer
	c := Load(Source{}, slog.New(slog.NewTextHandler(&logs, nil)))
	observability.ObserveLicense(c.Info().Valid, c.Info().ExpiresAt)
	require.True(t, c.IsEnabled(FeatureSSO))
	require.Equal(t, float64(1), testutil.ToFloat64(observability.LicenseValid))

	prevNow := nowFunc
	nowFunc = func() time.Time { return exp.Add(time.Minute) }
	t.Cleanup(func() { nowFunc = prevNow })

	for _, f := range []Feature{FeatureSSO, FeatureAuditEgress, FeatureGovernanceAdvanced} {
		assert.Falsef(t, c.IsEnabled(f), "%q must switch off once the licence lapses", f)
		assert.False(t, c.IsEnabled(f))
	}
	assert.Equal(t, TierCore, c.Tier())
	info := c.Info()
	assert.False(t, info.Valid)
	assert.Contains(t, info.Reason, "expired")

	assert.Equal(t, 1, strings.Count(logs.String(), "license.lapsed"), "exactly one WARN on lapse")
	assert.Contains(t, logs.String(), "level=WARN msg=license.lapsed")
	assert.Equal(t, float64(0), testutil.ToFloat64(observability.LicenseValid), "the validity gauge drops on lapse")
}

func TestChecker_UnexpiredLicenceStaysOn(t *testing.T) {
	pub, priv := newTestKeypair(t)
	exp := time.Now().Add(time.Hour)
	tok := signTestToken(t, priv, TierTeam, exp)
	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })
	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)

	c := Load(Source{}, silentLogger())
	prevNow := nowFunc
	nowFunc = func() time.Time { return exp.Add(-time.Second) }
	t.Cleanup(func() { nowFunc = prevNow })
	assert.True(t, c.IsEnabled(FeatureSSO))
	assert.Equal(t, TierTeam, c.Tier())
	assert.True(t, c.Info().Valid)
}
