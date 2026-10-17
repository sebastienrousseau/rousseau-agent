package config

import (
	"time"
)

// AuthConfig groups authentication surfaces. Today only SSO has
// operator-facing knobs; other auth paths (static allowlist, local
// SQLite) live under their respective transport / state config.
type AuthConfig struct {
	SSO SSOConfig `mapstructure:"sso"`
}

// SSOConfig configures the enterprise SSO surface. Zero value
// leaves SSO disabled — the OSS static-allowlist path handles
// every request. Requires FeatureSSO on the licence to activate;
// see docs/COMMERCIAL.md §2.1.
type SSOConfig struct {
	// Kind selects the SSO backend. Empty (default) leaves SSO
	// disabled; "oidc" enables the OpenID Connect verifier.
	Kind string `mapstructure:"kind"`
	// OIDC configures the OIDC verifier. Ignored when kind != "oidc".
	OIDC SSOOIDCConfig `mapstructure:"oidc"`
	// BindingTTL bounds how long a /login binding stays valid on
	// the daemon side. Empty (default zero) uses the token's exp
	// claim as-is; a shorter TTL clips a long-lived token so a
	// mis-issued 1-year JWT doesn't unlock a chat identity for a
	// year. Recommended: 24h.
	BindingTTL time.Duration `mapstructure:"binding_ttl"`
	// SCIM configures the SCIM 2.0 Service Provider — the pull-
	// based counterpart to the /login bootstrap. Fills the
	// ResolveTransportID deferral from #114 by letting an IdP
	// push users + groups on their existing SCIM schedule. Zero
	// value leaves the SCIM server off. Requires FeatureSSO.
	SCIM SCIMConfig `mapstructure:"scim"`
}

// SCIMConfig configures the SCIM 2.0 Service Provider.
type SCIMConfig struct {
	// Addr binds the SCIM HTTP endpoint. Empty leaves the
	// server off. Standard IdPs expect https://: set TLSCertFile
	// + TLSKeyFile, or bind loopback behind a TLS-terminating
	// reverse proxy. A non-loopback Addr without TLS leaves the
	// server off unless AllowPlaintext is set.
	Addr string `mapstructure:"addr"`
	// BearerToken is the shared secret the IdP presents in
	// the Authorization: Bearer header. Required when Addr is
	// set. Rotate via secret-manager / env var.
	BearerToken string `mapstructure:"bearer_token"`
	// BaseURL is the daemon's externally-reachable URL used
	// for the SCIM Meta.Location field. Optional; empty uses
	// relative /scim/v2/... paths.
	BaseURL string `mapstructure:"base_url"`
	// TLSCertFile and TLSKeyFile are the PEM certificate chain and
	// private key the SCIM endpoint terminates TLS with. Set both or
	// neither.
	TLSCertFile string `mapstructure:"tls_cert_file"`
	TLSKeyFile  string `mapstructure:"tls_key_file"`
	// AllowPlaintext permits a non-loopback Addr without TLS (a
	// reverse proxy on a private network terminates TLS). Without it
	// a non-loopback plaintext bind leaves the SCIM server off.
	AllowPlaintext bool `mapstructure:"allow_plaintext"`
}

// SSOOIDCConfig is the operator-facing view of internal/auth/sso's
// OIDCConfig. Kept as a separate type so the config schema and
// the wire type can evolve independently — mapstructure tags stay
// in one place.
type SSOOIDCConfig struct {
	// Issuer is the IdP's issuer URL (e.g. https://tenant.okta.com).
	// Required. The verifier fetches /.well-known/openid-configuration
	// under this base at first VerifyToken call.
	Issuer string `mapstructure:"issuer"`
	// Audience is the expected `aud` claim on inbound tokens: this
	// daemon's client id at the IdP. Required unless
	// AllowAnyAudience is set.
	Audience string `mapstructure:"audience"`
	// AllowAnyAudience skips the `aud` check. Only for an issuer that
	// serves this daemon alone: otherwise a token the IdP issued for
	// any of its other clients signs a user in here.
	AllowAnyAudience bool `mapstructure:"allow_any_audience"`
	// JWKSAllowedHosts (sso.oidc.jwks_allowed_hosts) lists extra hosts
	// the discovery document's jwks_uri may name, for IdPs that serve
	// signing keys from a host other than the issuer's. Empty admits
	// the issuer's host only. jwks_uri must be https (plain http is
	// accepted for loopback hosts only).
	JWKSAllowedHosts []string `mapstructure:"jwks_allowed_hosts"`
	// JWKSRefresh controls how often the verifier re-fetches the
	// IdP's JWKS. Zero uses the shipped 15-minute default.
	JWKSRefresh time.Duration `mapstructure:"jwks_refresh"`
	// ClockSkew tolerates NTP drift between the IdP and the
	// daemon. Zero uses the shipped 2-minute default.
	ClockSkew time.Duration `mapstructure:"clock_skew"`
	// TransportMappings maps custom / namespaced token claims to
	// per-transport IDs. Example: `{transport: slack, claim_key:
	// "https://schemas.example.com/slack_user_id"}` lifts an
	// Okta-issued Slack ID from the JWT into the resulting
	// Identity.TransportIDs["slack"] field.
	TransportMappings []SSOTransportMapping `mapstructure:"transport_mappings"`
}

// SSOTransportMapping is one entry in
// [SSOOIDCConfig.TransportMappings].
type SSOTransportMapping struct {
	Transport string `mapstructure:"transport"`
	ClaimKey  string `mapstructure:"claim_key"`
}
