package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func TestCheckSSO_UnconfiguredEmitsNothing(t *testing.T) {
	// Vast-majority OSS install: SSO not configured → zero rows.
	// Keeps the doctor output tight.
	got := checkSSO(context.Background(), &config.Config{}, license.Core())
	assert.Empty(t, got, "SSO rows must not appear when SSO isn't configured")
}

func TestCheckSSO_ConfiguredWithoutLicenceWarns(t *testing.T) {
	// The most common misconfiguration: operator sets auth.sso in
	// config.yaml but hasn't attached a licence. The daemon boots
	// but /login is inert — doctor must surface that loudly.
	got := checkSSO(context.Background(), &config.Config{
		Auth: config.AuthConfig{SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{Issuer: "https://tenant.okta.com"},
		}},
	}, license.Core())

	var byName = make(map[string]diagResult, len(got))
	for _, r := range got {
		byName[r.Name] = r
	}
	require.Contains(t, byName, "identity.sso.kind")
	assert.Equal(t, "oidc", byName["identity.sso.kind"].Detail)

	require.Contains(t, byName, "identity.sso.issuer")
	assert.Equal(t, "info", byName["identity.sso.issuer"].Status)

	require.Contains(t, byName, "identity.sso.licensed")
	assert.Equal(t, "warn", byName["identity.sso.licensed"].Status,
		"unlicensed but configured must be a warn — the operator paid for a config that isn't doing anything")
}

func TestCheckSSO_KindOIDCWithoutIssuerFails(t *testing.T) {
	got := checkSSO(context.Background(), &config.Config{
		Auth: config.AuthConfig{SSO: config.SSOConfig{Kind: "oidc"}},
	}, license.Core())
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail, "kind=oidc without issuer must fail")
}

func TestCheckSSO_LicensedRendersOK(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-abc",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkSSO(context.Background(), &config.Config{
		Auth: config.AuthConfig{SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{Issuer: "https://tenant.okta.com"},
		}},
	}, chk)
	var licensedStatus string
	for _, r := range got {
		if r.Name == "identity.sso.licensed" {
			licensedStatus = r.Status
		}
	}
	assert.Equal(t, "ok", licensedStatus)
}

func TestCheckSSO_BindingsCountRendersWhenSqlite(t *testing.T) {
	// Full end-to-end: seed a bindings row via the real
	// SQLite store, then confirm doctor's checkSSO surfaces
	// the count.
	dir := t.TempDir()
	dbPath := dir + "/sessions.db"

	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, dbPath)
	require.NoError(t, err)
	bindings, err := sqlitestore.NewSSOBindings(ctx, store)
	require.NoError(t, err)
	require.NoError(t, bindings.Bind(ctx, "whatsapp", "+42",
		sso.Identity{Subject: "okta|test"},
		time.Now().Add(time.Hour)))
	require.NoError(t, store.Close())

	got := checkSSO(ctx, &config.Config{
		State: config.StateConfig{Path: dbPath},
		Auth: config.AuthConfig{SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{Issuer: "https://tenant.okta.com"},
		}},
	}, license.Core())

	var haveBindings bool
	for _, r := range got {
		if r.Name == "identity.sso.bindings" {
			haveBindings = true
			assert.Contains(t, r.Detail, "1 active")
		}
	}
	assert.True(t, haveBindings)
}

func TestCheckSSO_PostgresDriverSkipsBindingsCount(t *testing.T) {
	// Postgres binding lookup is a follow-up; doctor should
	// simply omit the row rather than blast an error.
	got := checkSSO(context.Background(), &config.Config{
		State: config.StateConfig{Driver: "postgres", DSN: "postgres://x"},
		Auth: config.AuthConfig{SSO: config.SSOConfig{
			Kind: "oidc",
			OIDC: config.SSOOIDCConfig{Issuer: "https://tenant.okta.com"},
		}},
	}, license.Core())
	for _, r := range got {
		assert.NotEqual(t, "identity.sso.bindings", r.Name)
	}
}
