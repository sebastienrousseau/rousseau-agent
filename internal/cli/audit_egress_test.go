package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// -- buildAuditSink --

func TestBuildAuditSink_UnconfiguredReturnsNop(t *testing.T) {
	// Zero-config OSS path: no sink activity at all — the
	// operator hasn't asked for one, we don't build one.
	sink, err := buildAuditSink(config.AuditEgressConfig{}, license.Core(), nil, silentLogger())
	require.NoError(t, err)
	require.NotNil(t, sink)
	_, isNop := sink.(audit_egress.Nop)
	assert.True(t, isNop, "empty config must return Nop")
}

func TestBuildAuditSink_UnlicensedReturnsNop(t *testing.T) {
	// Configured but licence doesn't unlock → Nop. The user-
	// facing signal is a doctor warn row + INFO log; the sink
	// itself is silent.
	sink, err := buildAuditSink(config.AuditEgressConfig{
		Kind:     "otlp_http",
		Endpoint: "https://siem.example.com/v1/logs",
	}, license.Core(), nil, silentLogger())
	require.NoError(t, err)
	_, isNop := sink.(audit_egress.Nop)
	assert.True(t, isNop, "configured but unlicensed must return Nop")
}

func TestBuildAuditSink_LicensedNoChainReturnsOTLPSink(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-audit",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	sink, err := buildAuditSink(config.AuditEgressConfig{
		Kind:     "otlp_http",
		Endpoint: "https://siem.example.com/v1/logs",
	}, chk, nil, silentLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(closeCtx) //nolint:errcheck // test cleanup
	})
	// The wrap is off — the sink must be the raw OTLP one, not
	// a ChainedSink.
	_, isChained := sink.(*audit_egress.ChainedSink)
	assert.False(t, isChained)
	_, isNop := sink.(audit_egress.Nop)
	assert.False(t, isNop)
}

func TestBuildAuditSink_ChainedFlagWrapsInChainedSink(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-audit",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	sink, err := buildAuditSink(config.AuditEgressConfig{
		Kind:     "otlp_http",
		Endpoint: "https://siem.example.com/v1/logs",
		Chained:  true,
	}, chk, nil, silentLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(closeCtx) //nolint:errcheck // test cleanup
	})
	_, isChained := sink.(*audit_egress.ChainedSink)
	assert.True(t, isChained, "chained=true must wrap the sink in ChainedSink")
}

func TestBuildAuditSink_ChainedFlagOnNopIsNop(t *testing.T) {
	// Chaining a Nop wastes hash computation on records that
	// never leave the process. buildAuditSink short-circuits:
	// Nop stays Nop even when chained=true is set.
	sink, err := buildAuditSink(config.AuditEgressConfig{
		Kind:    "otlp_http",
		Chained: true,
	}, license.Core(), nil, silentLogger())
	require.NoError(t, err)
	_, isChained := sink.(*audit_egress.ChainedSink)
	assert.False(t, isChained)
	_, isNop := sink.(audit_egress.Nop)
	assert.True(t, isNop)
}

// -- checkAuditEgress doctor rows --

func TestCheckAuditEgress_UnconfiguredEmitsNothing(t *testing.T) {
	got := checkAuditEgress(&config.Config{}, license.Core())
	assert.Empty(t, got, "no rows on OSS install")
}

func TestCheckAuditEgress_UnlicensedWarns(t *testing.T) {
	got := checkAuditEgress(&config.Config{Observability: config.ObservabilityConfig{
		AuditEgress: config.AuditEgressConfig{
			Kind:     "otlp_http",
			Endpoint: "https://siem.example.com/v1/logs",
		},
	}}, license.Core())

	var byName = make(map[string]diagResult, len(got))
	for _, r := range got {
		byName[r.Name] = r
	}
	require.Contains(t, byName, "observability.audit_egress.kind")
	require.Contains(t, byName, "observability.audit_egress.licensed")
	assert.Equal(t, "warn", byName["observability.audit_egress.licensed"].Status)
}

func TestCheckAuditEgress_OTLPWithoutEndpointFails(t *testing.T) {
	got := checkAuditEgress(&config.Config{Observability: config.ObservabilityConfig{
		AuditEgress: config.AuditEgressConfig{Kind: "otlp_http"},
	}}, license.Core())
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail, "kind=otlp_http without endpoint must fail")
}

func TestCheckAuditEgress_LicensedReportsOK(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-audit",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkAuditEgress(&config.Config{Observability: config.ObservabilityConfig{
		AuditEgress: config.AuditEgressConfig{
			Kind:     "otlp_http",
			Endpoint: "https://siem.example.com/v1/logs",
		},
	}}, chk)
	var status string
	for _, r := range got {
		if r.Name == "observability.audit_egress.licensed" {
			status = r.Status
		}
	}
	assert.Equal(t, "ok", status)
}

func TestCheckAuditEgress_ChainedFlagSurfaces(t *testing.T) {
	got := checkAuditEgress(&config.Config{Observability: config.ObservabilityConfig{
		AuditEgress: config.AuditEgressConfig{
			Kind:     "otlp_http",
			Endpoint: "https://siem.example.com/v1/logs",
			Chained:  true,
		},
	}}, license.Core())
	var haveChained diagResult
	for _, r := range got {
		if r.Name == "observability.audit_egress.chained" {
			haveChained = r
		}
	}
	assert.Equal(t, "yes (tamper-evident hash chain)", haveChained.Detail)
}

// TestBuildAuditSink_ChainKeyFailsClosed pins that a configured but
// unusable chain key stops startup instead of silently dropping the
// keyed MAC, and that a good key produces a chained sink.
func TestBuildAuditSink_ChainKeyFailsClosed(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject: "cust-audit", Tier: license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	dir := t.TempDir()
	short := filepath.Join(dir, "short.key")
	require.NoError(t, os.WriteFile(short, []byte("too-short\n"), 0o600))
	good := filepath.Join(dir, "good.key")
	require.NoError(t, os.WriteFile(good, []byte("0123456789abcdef0123456789abcdef\n"), 0o600))

	base := config.AuditEgressConfig{Kind: "otlp_http", Endpoint: "https://siem.example.com/v1/logs", Chained: true}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "nope.key"), "short": short} {
		cfg := base
		cfg.ChainHMACKeyFile = path
		_, err := buildAuditSink(cfg, chk, nil, silentLogger())
		assert.Error(t, err, name)
	}

	cfg := base
	cfg.ChainHMACKeyFile = good
	sink, err := buildAuditSink(cfg, chk, nil, silentLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(closeCtx) //nolint:errcheck // test cleanup
	})
	_, isChained := sink.(*audit_egress.ChainedSink)
	assert.True(t, isChained)
}
