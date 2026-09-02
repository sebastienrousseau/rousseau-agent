package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

const testAllowPolicy = `package rousseau.authz
import rego.v1
default allow := true
default reason := ""`

const testDenyPolicy = `package rousseau.authz
import rego.v1
default allow := false
default reason := "policy denied"`

// -- wrapWithOPA --

func TestWrapWithOPA_NoPolicyFileReturnsInner(t *testing.T) {
	// Zero-config path — no wrap. Property: operator pays no
	// OPA overhead when they haven't asked for it.
	inner := agent.DenyAllApprover{Reason: "inner"}
	got := wrapWithOPA(context.Background(), inner, config.OPAConfig{}, license.Core(), silentLogger())
	assert.Equal(t, inner, got)
}

func TestWrapWithOPA_UnlicensedFallsBackToInner(t *testing.T) {
	// Configured policy but licence doesn't unlock → the
	// original approver runs. Never silently start denying.
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.rego")
	require.NoError(t, os.WriteFile(path, []byte(testDenyPolicy), 0o600))

	inner := agent.AllowAllApprover{}
	got := wrapWithOPA(context.Background(), inner, config.OPAConfig{PolicyFile: path},
		license.Core(), silentLogger())

	// If wrap DID take effect, "bash" would deny. Fallback →
	// allow-all runs → decision is allow.
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithOPA_MissingPolicyFileFallsBackToInner(t *testing.T) {
	// Fail-safe: a missing file must NOT take the daemon
	// offline. The operator sees a doctor fail row + WARN log.
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := wrapWithOPA(context.Background(), agent.AllowAllApprover{},
		config.OPAConfig{PolicyFile: "/does/not/exist.rego"},
		chk, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision, "missing policy file must not silently start denying")
}

func TestWrapWithOPA_BadPolicyFallsBackToInner(t *testing.T) {
	// Parse-time compile error → fall back to inner. Broken
	// Rego must never take the daemon offline.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.rego")
	require.NoError(t, os.WriteFile(path, []byte("this is not valid rego {{{"), 0o600))

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := wrapWithOPA(context.Background(), agent.AllowAllApprover{},
		config.OPAConfig{PolicyFile: path}, chk, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithOPA_LicensedAllowPolicyPassesThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.rego")
	require.NoError(t, os.WriteFile(path, []byte(testAllowPolicy), 0o600))

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := wrapWithOPA(context.Background(), agent.AllowAllApprover{},
		config.OPAConfig{PolicyFile: path}, chk, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithOPA_LicensedDenyPolicyBlocks(t *testing.T) {
	// Full activation. Policy denies → agent.DecisionDeny with
	// the "governance:" prefix.
	dir := t.TempDir()
	path := filepath.Join(dir, "deny.rego")
	require.NoError(t, os.WriteFile(path, []byte(testDenyPolicy), 0o600))

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := wrapWithOPA(context.Background(), agent.AllowAllApprover{},
		config.OPAConfig{PolicyFile: path}, chk, silentLogger())
	decision, reason := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "governance:")
	assert.Contains(t, reason, "policy denied")
}

func TestWrapWithOPA_NilCheckerActsLikeUnlicensed(t *testing.T) {
	// Defensive: a nil checker (upstream wiring bug) MUST NOT
	// activate the gate silently.
	dir := t.TempDir()
	path := filepath.Join(dir, "deny.rego")
	require.NoError(t, os.WriteFile(path, []byte(testDenyPolicy), 0o600))

	got := wrapWithOPA(context.Background(), agent.AllowAllApprover{},
		config.OPAConfig{PolicyFile: path}, nil, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

// -- checkGovernance covers OPA rows too --

func TestCheckGovernance_OPARowsSurfaceWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.rego")
	require.NoError(t, os.WriteFile(path, []byte(testAllowPolicy), 0o600))

	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{
			OPA: config.OPAConfig{PolicyFile: path},
		},
	}}, license.Core())

	var haveFile, haveLicensed bool
	for _, r := range got {
		if r.Name == "identity.governance.opa.policy_file" {
			haveFile = true
			assert.Equal(t, path, r.Detail)
			assert.Equal(t, "info", r.Status, "policy file exists → info, not fail")
		}
		if r.Name == "identity.governance.opa.licensed" {
			haveLicensed = true
			assert.Equal(t, "warn", r.Status, "unlicensed OPA config must warn")
		}
	}
	assert.True(t, haveFile)
	assert.True(t, haveLicensed)
}

func TestCheckGovernance_MissingOPAPolicyFileIsFail(t *testing.T) {
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{
			OPA: config.OPAConfig{PolicyFile: "/does/not/exist.rego"},
		},
	}}, license.Core())
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail, "missing OPA policy file must be surfaced as fail")
}

func TestCheckGovernance_LicensedOPAReportsOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.rego")
	require.NoError(t, os.WriteFile(path, []byte(testAllowPolicy), 0o600))

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{
			OPA: config.OPAConfig{PolicyFile: path},
		},
	}}, chk)
	var licensedStatus string
	for _, r := range got {
		if r.Name == "identity.governance.opa.licensed" {
			licensedStatus = r.Status
		}
	}
	assert.Equal(t, "ok", licensedStatus)
}

func TestCheckGovernance_UnconfiguredOPAEmitsNothingWhenAlsoNoRBAC(t *testing.T) {
	// No RBAC rules + no OPA policy file → completely quiet.
	got := checkGovernance(&config.Config{}, license.Core())
	assert.Empty(t, got)
}
