// Package license is the runtime seam that separates the free OSS
// core from the paid Enterprise/Team Edition inside a single static
// rousseau-agent binary.
//
// The design is deliberate:
//
//   - The SAME static binary contains every code path. Enterprise
//     features are not compiled out — they are runtime-gated by a
//     [Checker]. This preserves the "no phone home, no license
//     server, no separate binary distribution" promise while still
//     capturing enterprise budgets.
//   - Verification is 100% offline. A short JWS-like token
//     (base64url payload + Ed25519 signature) is passed via
//     ROUSSEAU_LICENSE_KEY (or a mode-0600 file); the Verifier
//     checks the signature against public keys embedded in the
//     binary at build time.
//   - Absent / invalid / expired tokens fall through to [Core], the
//     "free tier" checker: every enterprise feature reports
//     disabled and the daemon boots normally.
//   - No feature flag ever silently upgrades on a missing key. The
//     Checker.Info snapshot names the reason (missing / expired /
//     bad signature / bad payload) so operators can distinguish a
//     paying-customer misconfiguration from an OSS install.
//
// Gated surfaces (see docs/COMMERCIAL.md for the full definition):
//
//   - [FeatureSSO] — OIDC / SAML / corporate directory sync.
//   - [FeatureAuditEgress] — streaming audit logs to SIEM (Splunk,
//     Datadog, OTLP push) + immutable log format.
//   - [FeatureGovernanceAdvanced] — RBAC, Open Policy Agent, and
//     multi-party approval workflows.
//
// See [`docs/COMMERCIAL.md`](../../docs/COMMERCIAL.md) for the
// business rationale and the boundary contract between the OSS
// core and the paid tier.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Tier identifies which product tier is active.
type Tier string

// Tier values, in ascending order of capability.
const (
	// TierCore is the free OSS default. No license required. Every
	// enterprise feature reports disabled.
	TierCore Tier = "core"
	// TierTeam is the paid workspace tier — meant for small teams
	// that need SSO but don't need advanced governance / SIEM egress.
	TierTeam Tier = "team"
	// TierEnterprise is the full paid tier — every gated feature
	// enabled subject to per-license feature allowlists.
	TierEnterprise Tier = "enterprise"
)

// Feature is a stable identifier for a license-gated capability. The
// string values are wire-facing (they appear in signed license
// payloads) — never rename without a compat shim.
type Feature string

// Feature identifiers. Grouped roughly by the three commercial
// boundaries in docs/COMMERCIAL.md.
const (
	// FeatureSSO gates OIDC / SAML identity providers and corporate
	// directory sync. Core keeps local SQLite auth + API keys.
	FeatureSSO Feature = "sso"
	// FeatureAuditEgress gates streaming audit logs to external SIEMs
	// (Splunk, Datadog, OTLP push) and the immutable log format
	// required by common compliance regimes. Core keeps stdout +
	// SQLite-persisted session history.
	FeatureAuditEgress Feature = "audit_egress"
	// FeatureGovernanceAdvanced gates RBAC, Open Policy Agent
	// integration, and multi-party approval workflows. Core keeps
	// PatternApprover + the interactive TUI approver.
	FeatureGovernanceAdvanced Feature = "governance_advanced"
)

// Checker is the runtime seam every enterprise feature guards on.
// The shipped default (see [Core]) reports every feature disabled;
// a valid license swaps in a checker that reports enabled per its
// tier + feature allowlist.
//
// Implementations MUST be safe for concurrent use — the Checker is
// consulted from every goroutine that enters a gated code path.
type Checker interface {
	// IsEnabled reports whether f is unlocked by the active license.
	IsEnabled(f Feature) bool
	// Tier returns the currently-active tier. Callers that need to
	// distinguish core from team from enterprise (for feature-detect
	// UX, not access control) use this.
	Tier() Tier
	// Info returns a snapshot suitable for logs and the doctor
	// command. Never returns the raw token or the signature — it
	// exists so operators can debug "why isn't SSO on?" without
	// leaking their license key.
	Info() Info
}

// Info is the operator-facing snapshot of the active license.
type Info struct {
	// Tier is the currently-active tier.
	Tier Tier
	// Subject is the customer identifier from the license payload
	// (opaque — an internal customer ID, not an email). Empty on
	// the core tier.
	Subject string
	// Features lists the enabled features. Nil on core. When the
	// license payload's Features field is empty (meaning "every
	// feature in the tier"), this is populated with the effective
	// set so operators see what's actually on.
	Features []Feature
	// Valid is true when a license was found AND cryptographically
	// verified AND not expired. False for the core tier as well as
	// for every failure mode.
	Valid bool
	// Expiring is true when the license is valid but within
	// [ExpiryWarnWindow] of its expiry — a hint to remind operators
	// to renew before their SSO stops working.
	Expiring bool
	// ExpiresAt is the license's expiry timestamp. Zero on core or
	// when the token is unparseable.
	ExpiresAt time.Time
	// Reason is a short human-readable explanation of why Valid is
	// false. Empty when Valid is true. Non-empty examples:
	// "no license configured", "bad signature", "expired",
	// "malformed payload", "unknown key ID".
	Reason string
}

// ExpiryWarnWindow is the lead time before expiry at which the
// Checker starts flagging Info.Expiring = true. Chosen so a monthly
// operator review picks up renewals well before SSO breaks.
const ExpiryWarnWindow = 14 * 24 * time.Hour

// Source describes where to look for a license token. Empty Source
// looks at the default env var; explicit File wins over env when
// both are set (a file bind is the standard secret-injection
// pattern for systemd + Kubernetes).
type Source struct {
	// Env is the environment variable name. Empty defaults to
	// [DefaultEnvVar]. Set to "-" to disable env lookup entirely.
	Env string
	// File is a filesystem path. Empty disables file lookup. The
	// file must be mode-0600 (readable only by the daemon's UID);
	// looser modes are rejected to prevent a shared-machine leak.
	File string
}

// DefaultEnvVar is the environment variable rousseau reads a
// license from when Source.Env is empty.
const DefaultEnvVar = "ROUSSEAU_LICENSE_KEY"

// Core returns the shipped default checker: every enterprise
// feature reports disabled, [Info.Tier] is [TierCore]. Use this as
// the fallback anywhere a license is unavailable — never return
// nil to a code path that would call IsEnabled.
func Core() Checker {
	return &checker{
		tier: TierCore,
		info: Info{Tier: TierCore, Reason: "no license configured"},
	}
}

// Load reads a license from source, verifies it against the
// compiled-in public keys, and returns the resulting Checker. On
// any failure (missing token, bad signature, expired, malformed)
// Load returns [Core] with Info.Reason set to a diagnostic — the
// daemon boots regardless. Failures are logged at Warn so operators
// notice a paid-customer misconfiguration without the daemon
// hard-failing.
//
// A nil logger uses slog.Default.
func Load(source Source, logger *slog.Logger) Checker {
	if logger == nil {
		logger = slog.Default()
	}
	raw, srcName, err := readToken(source)
	if err != nil {
		logger.Warn("license.load_failed",
			slog.String("source", srcName),
			slog.String("err", err.Error()),
		)
		c := Core()
		c.(*checker).info.Reason = err.Error()
		return c
	}
	if raw == "" {
		// No license — the OSS default path. Logged at Debug so it
		// isn't noisy on the vast majority of installs.
		logger.Debug("license.absent", slog.String("source", srcName))
		return Core()
	}
	claims, err := verifyToken(raw, EmbeddedPublicKeys())
	if err != nil {
		logger.Warn("license.verify_failed",
			slog.String("source", srcName),
			slog.String("err", err.Error()),
		)
		c := Core()
		c.(*checker).info.Reason = err.Error()
		return c
	}
	if err := claims.Validate(time.Now()); err != nil {
		logger.Warn("license.expired",
			slog.String("subject", claims.Subject),
			slog.String("err", err.Error()),
		)
		c := Core()
		c.(*checker).info.Reason = err.Error()
		return c
	}
	logger.Info("license.loaded",
		slog.String("subject", claims.Subject),
		slog.String("tier", string(claims.Tier)),
		slog.Time("expires_at", time.Unix(claims.ExpiresAt, 0).UTC()),
	)
	return newChecker(claims)
}

// checker is the concrete Checker implementation. Immutable after
// construction — safe for concurrent use without a mutex.
type checker struct {
	tier     Tier
	features map[Feature]struct{}
	info     Info
}

func newChecker(c Claims) *checker {
	features := c.effectiveFeatures()
	set := make(map[Feature]struct{}, len(features))
	for _, f := range features {
		set[f] = struct{}{}
	}
	expiresAt := time.Unix(c.ExpiresAt, 0).UTC()
	return &checker{
		tier:     c.Tier,
		features: set,
		info: Info{
			Tier:      c.Tier,
			Subject:   c.Subject,
			Features:  features,
			Valid:     true,
			Expiring:  time.Until(expiresAt) < ExpiryWarnWindow,
			ExpiresAt: expiresAt,
		},
	}
}

// IsEnabled satisfies [Checker].
func (c *checker) IsEnabled(f Feature) bool {
	if c.features == nil {
		return false
	}
	_, ok := c.features[f]
	return ok
}

// Tier satisfies [Checker].
func (c *checker) Tier() Tier { return c.tier }

// Info satisfies [Checker].
func (c *checker) Info() Info { return c.info }

// Claims is the license JWT payload rousseau signs and verifies.
// Minimal by design — the fewer fields, the smaller the surface a
// customer might complain about.
type Claims struct {
	// Subject is an opaque customer identifier (internal ID or a
	// UUID — never an email or a display name that might change).
	Subject string `json:"sub"`
	// Tier is the product tier this license grants.
	Tier Tier `json:"tier"`
	// Features is the explicit feature allowlist. Empty means
	// "every feature in the tier" — see [Claims.effectiveFeatures].
	Features []Feature `json:"features,omitempty"`
	// IssuedAt is a Unix timestamp of when the license was signed.
	IssuedAt int64 `json:"iat"`
	// ExpiresAt is a Unix timestamp beyond which the license is
	// invalid. Zero means never expires — reserved for lifetime
	// licenses (rare; documented separately).
	ExpiresAt int64 `json:"exp"`
	// Seats is the maximum concurrent user count this license
	// permits. Zero means unlimited within the license. Enforcement
	// is left to individual features that care about seat counts
	// (e.g., SSO); the Checker itself does not police seats.
	Seats int `json:"seats,omitempty"`
}

// Validate reports whether the claims are valid at now. Bad tier,
// zero expiry (unless issued as an explicit lifetime license — not
// supported here), and expired-past-now are all rejected.
func (c Claims) Validate(now time.Time) error {
	if c.Tier != TierTeam && c.Tier != TierEnterprise {
		return fmt.Errorf("license: unknown tier %q", c.Tier)
	}
	if c.ExpiresAt == 0 {
		return errors.New("license: missing expiry")
	}
	if time.Unix(c.ExpiresAt, 0).Before(now) {
		return fmt.Errorf("license: expired at %s", time.Unix(c.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	return nil
}

// effectiveFeatures returns the concrete feature list this license
// unlocks. An empty [Claims.Features] means "every feature in the
// tier" — the tier-default mapping lives here so a license issued
// today automatically picks up a feature added tomorrow (as long as
// the customer's tier is high enough).
func (c Claims) effectiveFeatures() []Feature {
	if len(c.Features) > 0 {
		out := make([]Feature, len(c.Features))
		copy(out, c.Features)
		return out
	}
	switch c.Tier {
	case TierTeam:
		// Team = SSO only. Audit egress + advanced governance are
		// enterprise-only per docs/COMMERCIAL.md.
		return []Feature{FeatureSSO}
	case TierEnterprise:
		return []Feature{FeatureSSO, FeatureAuditEgress, FeatureGovernanceAdvanced}
	default:
		return nil
	}
}

// readToken pulls the raw license string from the configured source
// (env var first, then file). Returns the token, a source label for
// logs, and an error if the file's permissions are unsafe.
func readToken(s Source) (raw, src string, err error) {
	env := s.Env
	if env == "" {
		env = DefaultEnvVar
	}
	if env != "-" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, "env:" + env, nil
		}
	}
	if s.File == "" {
		return "", "env:" + env + " (empty)", nil
	}
	info, err := os.Stat(s.File)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "file:" + s.File + " (absent)", nil
		}
		return "", "file:" + s.File, fmt.Errorf("stat: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", "file:" + s.File, fmt.Errorf("license file %s has permissive mode %o (want 0600)", s.File, mode)
	}
	b, err := os.ReadFile(s.File) //nolint:gosec // path is operator-supplied config
	if err != nil {
		return "", "file:" + s.File, fmt.Errorf("read: %w", err)
	}
	return strings.TrimSpace(string(b)), "file:" + s.File, nil
}

// verifyToken splits a token into payload + signature, verifies the
// Ed25519 signature against every provided public key (any match
// wins), and unmarshals the payload into Claims.
//
// Token format: base64url(payload) + "." + base64url(signature).
// Chosen over full JWT for two reasons: (a) no algorithm negotiation
// means no "alg=none" downgrade footgun, (b) a two-part token is
// half the code surface — everything below the sig is application
// data, no header to argue about.
func verifyToken(token string, keys []ed25519.PublicKey) (Claims, error) {
	if len(keys) == 0 {
		return Claims{}, errors.New("license: no verification keys embedded")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errors.New("license: token must be <payload>.<sig>")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("license: payload base64: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("license: signature base64: %w", err)
	}
	verified := false
	for _, k := range keys {
		if ed25519.Verify(k, payload, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return Claims{}, errors.New("license: signature does not verify against any embedded key")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, fmt.Errorf("license: payload json: %w", err)
	}
	return c, nil
}

// SignPayload is a convenience for the release toolchain that issues
// licenses. Not called from the daemon — kept in-package so the
// signing + verification code live together and drift is impossible.
func SignPayload(c Claims, priv ed25519.PrivateKey) (string, error) {
	if c.IssuedAt == 0 {
		c.IssuedAt = time.Now().UTC().Unix()
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("license: sign payload marshal: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
