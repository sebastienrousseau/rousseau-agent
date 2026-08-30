package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
)

// RawKeys is the ldflag-injected keyring the daemon trusts to sign
// license tokens. Comma-separated list of base64-standard-encoded
// Ed25519 public keys. Multiple keys support rotation (or an
// emergency revocation) without a coordinated flag-day for every
// deployed daemon.
//
// # Key management
//
// The build supplies the real key(s) via one of two paths:
//
//   - -ldflags "-X 'internal/license.RawKeys=<b64>[,<b64>]'" for a
//     drop-in override at build time (used by the release workflow).
//   - //go:embed of an optional keys/*.ed25519.pub bundle — not
//     shipped with the OSS repo (the signing infra lives in a
//     separate private repo), left as a documented option for a
//     future maintainer.
//
// If no keys are provided at build time (the default OSS build),
// EmbeddedPublicKeys returns an empty slice and every non-empty
// token fails verification with "no verification keys embedded".
// The daemon still boots on the core tier.
//
// # Security considerations
//
// The keys are PUBLIC — they are checked into the binary by
// design. Compromise of a public key means a legitimate customer
// cannot verify their license (a DoS on the paid tier), not that
// an attacker can forge one. The corresponding PRIVATE keys live
// in the release infra's HSM / secret manager and never touch this
// repository or a developer laptop.
//
// A key ROTATION is a normal quarterly operation: add the new
// public key to the ldflag list, cut a release, wait for customers
// to upgrade, then drop the old key on the next release. Never
// remove a key that active licenses were signed against — the
// deployed daemons will lose verification.
var RawKeys string //nolint:gochecknoglobals // ldflag-injected at build time

// EmbeddedPublicKeys returns the Ed25519 public keys this binary
// trusts for license verification. Empty in the shipped OSS build
// (RawKeys is set only by the private release workflow).
//
// Any parse failure on a key is silently skipped — an operator
// who accidentally injects a malformed key at build time still
// gets a functional daemon on the core tier. The rest of the
// slice is intact so a partial-corruption incident does not brick
// the whole keyring.
func EmbeddedPublicKeys() []ed25519.PublicKey {
	raw := strings.TrimSpace(RawKeys)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]ed25519.PublicKey, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			continue
		}
		if len(b) != ed25519.PublicKeySize {
			continue
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out
}
