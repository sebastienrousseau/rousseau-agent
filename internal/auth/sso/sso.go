// Package sso implements the enterprise-only Single Sign-On surface
// — the second real gate wired against the licence seam from
// `internal/license`.
//
// # Boundary
//
// See [`docs/COMMERCIAL.md`](../../../docs/COMMERCIAL.md) §2.1.
// Local SQLite auth, API keys, and inherited `claude` CLI OAuth stay
// in the OSS core. What this package ships — OIDC / SAML identity
// resolution and corporate-directory mapping — is gated on
// [license.FeatureSSO].
//
// # Design
//
// The daemon has no HTTP surface of its own, so "SSO" here is not
// browser-flow SSO. It is:
//
//  1. Validating inbound JWT bearer tokens against a corporate IdP
//     (Okta, Entra ID, Auth0, Google Workspace, Keycloak, …). Used
//     by the future admin API and by MCP clients that speak
//     OAuth-Bearer.
//  2. Mapping transport-native identifiers (Slack `U…`, Matrix MXID,
//     Discord snowflake) to a canonical SSO identity via
//     claim-based lookup against a cached directory snapshot.
//     Enables RBAC / audit-trail continuity across transports for
//     the same corporate user.
//
// This PR ships (1) plus the [Directory] interface (2)'s downstream
// consumers plug into. A follow-up wires an actual directory-sync
// source (SCIM 2.0 pull or IdP-native API).
//
// # Fail-closed discipline
//
// Enterprise-only feature → no licence unlocks nothing:
//
//   - No configuration        → [Nop] directory, silent.
//   - Licence doesn't unlock  → [Nop] directory, ONE INFO log
//     line naming the licence-required path.
//   - Bad configuration       → [Nop] + WARN. Daemon boots
//     (audit-adjacent features must never take the agent offline;
//     the operator can debug via `rousseau doctor` without the
//     LLM going dark).
//
// Unlike [audit_egress] which fails OPEN on runtime errors (drop
// records rather than block), SSO fails CLOSED on runtime errors
// (reject the token rather than authorise). That asymmetry is by
// design — an audit gap is annoying, an authorisation bypass is a
// security incident.
package sso

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

// Kind selects the SSO backend implementation. String values are
// wire-facing (they appear in operator config); never rename
// without a compat shim.
type Kind string

// Kind constants.
const (
	// KindNone is the shipped default. No SSO configuration → no
	// directory → the OSS local-auth path handles every request.
	KindNone Kind = ""
	// KindOIDC validates JWT bearer tokens against an OpenID Connect
	// discovery-document + JWKS. Covers Okta, Entra ID, Auth0,
	// Google Workspace, Keycloak, Cognito — any IdP that speaks the
	// standard discovery + JWKS pattern.
	KindOIDC Kind = "oidc"
)

// Config configures the SSO subsystem. Zero-value Config leaves SSO
// disabled — the daemon boots on the OSS local-auth path.
type Config struct {
	// Kind selects the backend.
	Kind Kind `mapstructure:"kind"`
	// OIDC is populated when Kind == KindOIDC.
	OIDC OIDCConfig `mapstructure:"oidc"`
}

// OIDCConfig configures the OIDC verifier. The issuer URL is the
// only required field — every other setting can be auto-derived
// from the standard `/.well-known/openid-configuration` endpoint.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL (e.g.
	// "https://tenant.okta.com/oauth2/default"). The verifier
	// discovers the JWKS URI via
	// `${Issuer}/.well-known/openid-configuration`.
	Issuer string `mapstructure:"issuer"`
	// Audience is the expected `aud` claim on inbound tokens.
	// When set, the verifier rejects tokens whose audience doesn't
	// match. Empty disables the audience check (only sane in
	// single-tenant test setups).
	Audience string `mapstructure:"audience"`
	// JWKSRefresh bounds how often the verifier re-fetches the
	// IdP's JWKS. Zero uses [DefaultJWKSRefresh]. IdPs rotate
	// signing keys — too-long a refresh means legitimate tokens
	// reject after a rotation; too-short thrashes the IdP.
	JWKSRefresh time.Duration `mapstructure:"jwks_refresh"`
	// ClockSkew is the tolerance applied to `iat` / `exp` /
	// `nbf` checks. Zero uses [DefaultClockSkew].
	ClockSkew time.Duration `mapstructure:"clock_skew"`
	// TransportMappings is the claim-based lookup table for
	// resolving transport-native IDs to the canonical SSO subject.
	// See [TransportMapping].
	TransportMappings []TransportMapping `mapstructure:"transport_mappings"`
}

// TransportMapping tells the directory which token claim carries a
// transport's native identifier. Example: an Okta profile with a
// custom `slack_user_id` attribute is discoverable via
//
//	TransportMapping{Transport: "slack", ClaimKey: "slack_user_id"}
//
// so a Slack event from `U012ABCDE` looks up its SSO subject by
// searching for tokens whose `slack_user_id` claim == "U012ABCDE".
//
// Multiple mappings per transport are allowed (some IdPs stash the
// same value under different attribute names).
type TransportMapping struct {
	// Transport is the transport name ("slack", "matrix", "discord",
	// "telegram", etc). Matches the strings used in
	// `internal/identity`.
	Transport string `mapstructure:"transport"`
	// ClaimKey is the JWT claim name that carries the transport-
	// native identifier for this user.
	ClaimKey string `mapstructure:"claim_key"`
}

// Defaults.
const (
	// DefaultJWKSRefresh is the JWKS cache TTL. 15 minutes matches
	// every major IdP's key-rotation lead time.
	DefaultJWKSRefresh = 15 * time.Minute
	// DefaultClockSkew is the tolerance applied to time-based
	// claims. 2 minutes handles NTP drift on typical corporate
	// networks without opening a real replay window.
	DefaultClockSkew = 2 * time.Minute
)

// Identity is the canonical result of resolving a token or a
// transport-native identifier. Mirrors the OIDC standard claims
// (subject, email, name) plus rousseau-specific fields (groups
// for RBAC, transport IDs for cross-transport correlation).
type Identity struct {
	// Subject is the stable IdP-issued identifier (OIDC `sub`
	// claim). Never an email — emails change; `sub` doesn't.
	Subject string
	// Email is the user's primary email (OIDC `email` claim, when
	// present). Advisory — RBAC decisions MUST use Subject or
	// Groups, never Email.
	Email string
	// EmailVerified reflects the IdP's `email_verified` claim.
	// Only trust Email when this is true.
	EmailVerified bool
	// DisplayName is the human-readable name for logs and audit
	// trails. Best-effort — some IdPs don't populate `name`.
	DisplayName string
	// Groups are the IdP-issued group memberships. Feeds §2.9's
	// RBAC.
	Groups []string
	// TransportIDs maps each configured transport to the user's
	// native identifier there. Empty when no
	// [TransportMapping] resolved.
	TransportIDs map[string]string
	// ExpiresAt is the token's `exp` claim (populated only when
	// this identity came from a token, not a directory lookup).
	ExpiresAt time.Time
}

// Directory is the runtime seam every SSO-dependent code path
// consults. Two shapes: verify a token (inbound bearer auth) or
// resolve a transport identifier (identity-mapping for cross-
// transport correlation + RBAC).
type Directory interface {
	// VerifyToken validates a bearer token against the configured
	// IdP and returns the identity it represents. Returns
	// [ErrTokenInvalid] on signature failure, [ErrTokenExpired] on
	// `exp` / `nbf` violation, [ErrAudienceMismatch] on `aud`
	// mismatch, [ErrIssuerMismatch] on `iss` mismatch. Wraps
	// unexpected errors in a context wrapper.
	VerifyToken(ctx context.Context, token string) (Identity, error)
	// ResolveTransportID looks up a transport-native identifier
	// against the directory. Returns ([Identity]{}, [ErrNotFound])
	// when the identifier is not present. Callers use this to bind
	// an incoming chat message to its SSO subject.
	ResolveTransportID(ctx context.Context, transport, externalID string) (Identity, error)
}

// Sentinel errors for machine-readable failure handling.
var (
	// ErrTokenInvalid means the JWT signature or shape rejected —
	// wrong key, wrong algorithm, malformed. Callers surface this
	// as 401 to the client.
	ErrTokenInvalid = errors.New("sso: token signature invalid")
	// ErrTokenExpired means the token's time-based claims put it
	// outside the acceptance window. 401.
	ErrTokenExpired = errors.New("sso: token expired or not-yet-valid")
	// ErrAudienceMismatch means the token's `aud` claim doesn't
	// match the configured audience. 401 — an Auth0 token for a
	// different app isn't a rousseau token.
	ErrAudienceMismatch = errors.New("sso: token audience mismatch")
	// ErrIssuerMismatch means the token's `iss` claim doesn't
	// match the configured issuer. 401 — a token from one Okta
	// tenant is not valid for another.
	ErrIssuerMismatch = errors.New("sso: token issuer mismatch")
	// ErrNotFound means a transport identifier isn't in the
	// directory. Callers translate to "unknown user" in the
	// application-level auth check.
	ErrNotFound = errors.New("sso: identifier not found in directory")
	// ErrDirectoryDisabled means the caller invoked a Directory
	// method on the [Nop] fallback. Distinguishes "SSO not
	// configured" from "user unknown".
	ErrDirectoryDisabled = errors.New("sso: directory disabled (no licence or not configured)")
)

// Nop is the shipped default: every Directory method returns
// [ErrDirectoryDisabled]. Used when SSO isn't configured or the
// licence doesn't unlock it.
type Nop struct{}

// VerifyToken satisfies [Directory].
func (Nop) VerifyToken(context.Context, string) (Identity, error) {
	return Identity{}, ErrDirectoryDisabled
}

// ResolveTransportID satisfies [Directory].
func (Nop) ResolveTransportID(context.Context, string, string) (Identity, error) {
	return Identity{}, ErrDirectoryDisabled
}

// LicenseCheck is the narrow slice of [license.Checker] this
// package depends on. Extracted so tests substitute without
// pulling the full licence construction path.
type LicenseCheck interface {
	IsEnabled(feature license.Feature) bool
}

// New builds a [Directory] from cfg + checker. Returns [Nop] in the
// three documented no-op cases — no config, licence doesn't unlock,
// bad config — with the reason surfaced through logger. The daemon
// boots regardless; that's the fail-open discipline documented on
// the package.
//
// Nil logger uses [slog.Default].
func New(cfg Config, checker LicenseCheck, logger *slog.Logger) Directory {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Kind == KindNone {
		return Nop{}
	}
	if checker == nil || !checker.IsEnabled(license.FeatureSSO) {
		logger.Info("sso.license_required",
			slog.String("kind", string(cfg.Kind)),
			slog.String("feature", string(license.FeatureSSO)),
			slog.String("how_to_enable", "set ROUSSEAU_LICENSE_KEY to a Team or Enterprise licence — see docs/COMMERCIAL.md"),
		)
		return Nop{}
	}
	dir, err := build(cfg, logger)
	if err != nil {
		logger.Warn("sso.config_failed",
			slog.String("kind", string(cfg.Kind)),
			slog.String("err", err.Error()),
			slog.String("effect", "SSO is DISABLED — daemon is booting on the core local-auth path"),
		)
		return Nop{}
	}
	logger.Info("sso.started",
		slog.String("kind", string(cfg.Kind)),
		slog.String("issuer", cfg.OIDC.Issuer),
	)
	return dir
}

// build dispatches on Kind. Kept separate from New so the licence-
// check + logging shell stays trivially testable.
func build(cfg Config, logger *slog.Logger) (Directory, error) {
	switch cfg.Kind {
	case KindOIDC:
		return NewOIDCDirectory(cfg.OIDC, logger)
	default:
		return nil, fmt.Errorf("unknown sso kind %q", cfg.Kind)
	}
}

// applyDefaults folds every zero-value knob to its documented
// default. Called by every backend constructor.
func (c OIDCConfig) applyDefaults() OIDCConfig {
	if c.JWKSRefresh <= 0 {
		c.JWKSRefresh = DefaultJWKSRefresh
	}
	if c.ClockSkew <= 0 {
		c.ClockSkew = DefaultClockSkew
	}
	return c
}
