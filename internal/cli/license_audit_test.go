package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// captureAuditSink mirrors the pattern from
// internal/agent/audit_emit_test.go — records every Emit so
// tests can inspect the exact record shape.
type captureAuditSink struct {
	records []audit_egress.Record
}

func (c *captureAuditSink) Emit(_ context.Context, r audit_egress.Record) error {
	c.records = append(c.records, r)
	return nil
}
func (c *captureAuditSink) Close(context.Context) error { return nil }

// -- licenseSnapshotResult classifier --

func TestLicenseSnapshotResult_CoreIsCore(t *testing.T) {
	info := license.Info{Tier: license.TierCore, Reason: "no license configured"}
	assert.Equal(t, "core", licenseSnapshotResult(info))
}

func TestLicenseSnapshotResult_EmptyReasonIsCore(t *testing.T) {
	// Some code paths leave Reason empty on the core tier — must
	// still map to "core", not "invalid".
	info := license.Info{Tier: license.TierCore}
	assert.Equal(t, "core", licenseSnapshotResult(info))
}

func TestLicenseSnapshotResult_BadSignatureIsInvalid(t *testing.T) {
	// A configured-but-broken licence must land as "invalid"
	// so SIEM dashboards can alert on it. Distinct from "core".
	info := license.Info{
		Tier:   license.TierCore,
		Reason: "license: signature does not verify against any embedded key",
	}
	assert.Equal(t, "invalid", licenseSnapshotResult(info))
}

func TestLicenseSnapshotResult_ExpiredIsInvalid(t *testing.T) {
	info := license.Info{Tier: license.TierCore, Reason: "license: expired at ..."}
	assert.Equal(t, "invalid", licenseSnapshotResult(info))
}

func TestLicenseSnapshotResult_ValidExpiringIsExpiring(t *testing.T) {
	info := license.Info{
		Tier: license.TierEnterprise, Valid: true, Expiring: true,
		ExpiresAt: time.Now().Add(3 * 24 * time.Hour),
	}
	assert.Equal(t, "expiring", licenseSnapshotResult(info))
}

func TestLicenseSnapshotResult_ValidHealthyIsActive(t *testing.T) {
	info := license.Info{
		Tier: license.TierEnterprise, Valid: true,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour),
	}
	assert.Equal(t, "active", licenseSnapshotResult(info))
}

// -- emitLicenseSnapshot end-to-end --

func TestEmitLicenseSnapshot_CoreEmitsOneRecord(t *testing.T) {
	sink := &captureAuditSink{}
	emitLicenseSnapshot(sink, license.Core())
	require.Len(t, sink.records, 1)
	r := sink.records[0]
	assert.Equal(t, "license", r.Category)
	assert.Equal(t, "load", r.Verb)
	assert.Equal(t, "core", r.Object)
	assert.Equal(t, "core", r.Result)
	assert.Equal(t, "core", r.Detail["tier"])
	assert.Equal(t, false, r.Detail["valid"])
}

func TestEmitLicenseSnapshot_ValidLicenseFillsDetail(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-abc",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(180 * 24 * time.Hour).Unix(),
	})
	sink := &captureAuditSink{}
	emitLicenseSnapshot(sink, chk)
	require.Len(t, sink.records, 1)
	r := sink.records[0]
	assert.Equal(t, "active", r.Result)
	assert.Equal(t, "enterprise", r.Object)
	assert.Equal(t, "cust-abc", r.Detail["subject"])
	assert.Equal(t, true, r.Detail["valid"])
	assert.Contains(t, r.Detail, "expires_at")
	assert.Equal(t, false, r.Detail["expiring"])
	feats, ok := r.Detail["features"].([]string)
	require.True(t, ok)
	assert.Contains(t, feats, "sso")
	assert.Contains(t, feats, "audit_egress")
	assert.Contains(t, feats, "governance_advanced")
}

func TestEmitLicenseSnapshot_ExpiringLicenseFlagsIt(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-soon",
		Tier:      license.TierTeam,
		ExpiresAt: time.Now().Add(3 * 24 * time.Hour).Unix(),
	})
	sink := &captureAuditSink{}
	emitLicenseSnapshot(sink, chk)
	require.Len(t, sink.records, 1)
	r := sink.records[0]
	assert.Equal(t, "expiring", r.Result)
	assert.Equal(t, true, r.Detail["expiring"])
}

func TestEmitLicenseSnapshot_NilSinkIsNoop(t *testing.T) {
	// Property: nil sink is truly opt-out — no panic.
	require.NotPanics(t, func() {
		emitLicenseSnapshot(nil, license.Core())
	})
}

func TestEmitLicenseSnapshot_NilCheckerIsNoop(t *testing.T) {
	// Defensive: a nil checker (upstream wiring bug) must NOT
	// panic. Emit nothing so the operator sees "no licence
	// event" rather than a corrupted one.
	sink := &captureAuditSink{}
	require.NotPanics(t, func() {
		emitLicenseSnapshot(sink, nil)
	})
	assert.Empty(t, sink.records)
}

func TestEmitLicenseSnapshot_NopSinkStillCallsEmit(t *testing.T) {
	// The audit_egress.Nop path is a legal Sink implementation
	// — emitLicenseSnapshot must not type-assert-away legal
	// callers. Nop's Emit returns nil, no side effects.
	require.NotPanics(t, func() {
		emitLicenseSnapshot(audit_egress.Nop{}, license.Core())
	})
}
