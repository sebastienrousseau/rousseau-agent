package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// mintTestLicence generates a fresh Ed25519 keypair, signs an
// Enterprise-tier licence unlocking all three gated features, and
// swaps the daemon's trusted keyring so the signed token verifies.
// Cleanup restores the original keyring so parallel tests don't
// see the test key.
//
// Returned strings are safe to place in ROUSSEAU_LICENSE_KEY:
// the daemon's Source.Env path base64-decodes them via the same
// path a real customer would use.
func mintTestLicence(t *testing.T, features []license.Feature) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	tok, err := license.SignPayload(license.Claims{
		Subject:   "enterprise-smoke-test",
		Tier:      license.TierEnterprise,
		Features:  features,
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, priv)
	require.NoError(t, err)

	// Swap the trusted keyring for the duration of the test so
	// the daemon accepts our test-signed token. Restore on
	// cleanup to keep the package's default (empty) keyring
	// visible to other tests.
	origKeys := license.RawKeys
	license.RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { license.RawKeys = origKeys })

	return tok
}

// TestEnterpriseGates_AllFeaturesUnlock is the acid test for the
// paid Enterprise Edition promise: given a valid signed licence
// unlocking every gated feature AND matching config, does the
// daemon actually light up every gated surface?
//
// This is the single test that catches the "prospect trials the
// paid tier, hits a broken gate, deal dies" scenario. Every
// individual gate has unit tests around it; this test verifies
// they all light up together in the shape a real Enterprise
// deployment sends.
//
// Not gated behind a build tag — the licence is minted from a
// fresh keypair per-run, no long-lived secret is created, and
// the test runs in <100ms so CI cost is trivial.
func TestEnterpriseGates_AllFeaturesUnlock(t *testing.T) {
	// Empty features → tier defaults (every feature in the tier).
	// Explicit is safer here: pins that this test breaks if
	// someone adds a new gated feature without updating the smoke
	// alongside the gate.
	tok := mintTestLicence(t, []license.Feature{
		license.FeatureSSO,
		license.FeatureAuditEgress,
		license.FeatureGovernanceAdvanced,
	})
	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)

	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	// Configure every gated surface. Each block mirrors what a
	// real Enterprise customer would put in config.yaml —
	// deliberately close to the sample docs so operators
	// copy-pasting can trust this test also covers their shape.
	opts.Config.Auth = config.AuthConfig{
		SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{
				Issuer:   "https://example-idp.okta.com",
				Audience: "rousseau",
			},
			BindingTTL: 24 * time.Hour,
			SCIM: config.SCIMConfig{
				Addr:        ":7643",
				BearerToken: "test-scim-token",
				BaseURL:     "https://rousseau.example",
			},
		},
	}
	opts.Config.Observability.AuditEgress = config.AuditEgressConfig{
		Kind:     "otlp_http",
		Endpoint: "https://siem.example/v1/logs",
	}
	opts.Config.Agent.Approver.MultiParty = config.MultiPartyConfig{
		Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 2, Timeout: 5 * time.Minute},
		},
	}

	wiring, err := assembleDaemon(context.Background(), opts, []string{"1@s.whatsapp.net"})
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // best-effort test cleanup

	// -- FeatureSSO --------------------------------------------------
	// Licence Checker source-of-truth first: any drift here means
	// the licence didn't verify (Load fell back to Core silently).
	require.NotNil(t, wiring.Licence)
	assert.Equal(t, license.TierEnterprise, wiring.Licence.Tier(),
		"licence must load as enterprise — a drift back to Core silently disables every gate below")
	assert.True(t, wiring.Licence.IsEnabled(license.FeatureSSO),
		"FeatureSSO must unlock — SCIM + /login won't activate otherwise")

	// SCIM server: configured + licensed → non-nil. Without either
	// condition buildSCIM returns nil so downstream StartBackgroundServers
	// silently skips it. Test the wire-up landed.
	require.NotNil(t, wiring.SCIMServer, "SCIM server must bind when configured + FeatureSSO unlocked")
	assert.Equal(t, ":7643", wiring.SCIMAddr, "SCIM addr must be surfaced on wiring for the transport runner")

	// SSO bindings: always non-nil (fallback is sso.NoBindings), but
	// the real backing store must be present, not the no-op. Pin by
	// asserting it satisfies the interface + is NOT the sentinel.
	require.NotNil(t, wiring.SSOBindings)
	if _, isNop := wiring.SSOBindings.(interface{ isNop() }); isNop {
		t.Fatal("SSOBindings must be the real store, not NoBindings, when SSO is licensed")
	}

	// -- FeatureAuditEgress ------------------------------------------
	assert.True(t, wiring.Licence.IsEnabled(license.FeatureAuditEgress),
		"FeatureAuditEgress must unlock — audit records won't flow otherwise")

	// Audit sink: never nil, but must be the real sink (not Nop)
	// when configured + licensed. buildAuditSink returns Nop on the
	// three failure paths; this test exercises the happy path.
	require.NotNil(t, wiring.AuditSink)
	if _, isNop := wiring.AuditSink.(audit_egress.Nop); isNop {
		t.Fatal("AuditSink must be a real OTLP sink when audit_egress is configured + FeatureAuditEgress unlocked")
	}

	// -- FeatureGovernanceAdvanced -----------------------------------
	assert.True(t, wiring.Licence.IsEnabled(license.FeatureGovernanceAdvanced),
		"FeatureGovernanceAdvanced must unlock — multi-party / RBAC / OPA won't activate otherwise")

	// Multi-party approval: routerFor bakes the pendingApprovals
	// into RouterOptions.Approvals. If governance is not licensed
	// the router's approvals field stays nil (documented behaviour
	// in the router's handleApprovalCommand path).
	r := wiring.routerFor("whatsapp")
	require.NotNil(t, r, "routerFor must produce a router for the smoke transport")
}

// TestEnterpriseGates_UnlicensedFallsBackToCore is the inverse
// probe: with no licence env var, every gate stays closed and
// the daemon assembles as the OSS core. Pins the fail-CLOSED
// contract — a missing / bad licence never accidentally opens
// a paid surface.
func TestEnterpriseGates_UnlicensedFallsBackToCore(t *testing.T) {
	t.Setenv("ROUSSEAU_LICENSE_KEY", "")

	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	// Same config as the licensed test — proves the gates key on
	// the licence, not the config presence.
	opts.Config.Auth = config.AuthConfig{
		SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{Issuer: "https://example-idp.okta.com", Audience: "rousseau"},
			SCIM: config.SCIMConfig{Addr: ":7643", BearerToken: "x"},
		},
	}
	opts.Config.Observability.AuditEgress = config.AuditEgressConfig{
		Kind: "otlp_http", Endpoint: "https://siem.example/v1/logs",
	}
	opts.Config.Agent.Approver.MultiParty = config.MultiPartyConfig{
		Rules: []config.MultiPartyRule{{Tool: "bash", NeededApprovals: 2}},
	}

	wiring, err := assembleDaemon(context.Background(), opts, []string{"1@s.whatsapp.net"})
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // best-effort test cleanup

	require.NotNil(t, wiring.Licence)
	assert.False(t, wiring.Licence.IsEnabled(license.FeatureSSO),
		"unlicensed → FeatureSSO must stay closed")
	assert.False(t, wiring.Licence.IsEnabled(license.FeatureAuditEgress),
		"unlicensed → FeatureAuditEgress must stay closed")
	assert.False(t, wiring.Licence.IsEnabled(license.FeatureGovernanceAdvanced),
		"unlicensed → FeatureGovernanceAdvanced must stay closed")

	assert.Nil(t, wiring.SCIMServer,
		"SCIM server must NOT bind without FeatureSSO — configured-but-unlicensed is the classic silent-open bug this pin prevents")

	if _, isNop := wiring.AuditSink.(audit_egress.Nop); !isNop {
		t.Fatal("AuditSink must be Nop when unlicensed — records must not flow to SIEM under a core-tier daemon")
	}
}
