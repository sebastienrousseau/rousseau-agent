package skills

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
)

// BundleLoadOptions configures [LoadBundles].
type BundleLoadOptions struct {
	// TrustedPublisherKeys is the allow-list of Ed25519
	// public keys the loader accepts signatures from. A
	// bundle whose signature.public_key isn't in this list
	// is rejected — mirrors the license-package trust root.
	TrustedPublisherKeys []ed25519.PublicKey
	// Strict, when true, DROPS bundles that fail verification
	// (invalid signature, untrusted publisher, content hash
	// mismatch, malformed manifest). Recommended in
	// production. When false the loader still drops them but
	// only logs a WARN — no bundle is ever loaded unverified,
	// but a broken bundle in the dir doesn't abort startup.
	//
	// Note: unlike the plain-markdown loader, bundles have
	// NO "advisory mode" — an unverified bundle simply
	// doesn't load, no matter what. The Strict flag governs
	// only the log level.
	Strict bool
	// Logger receives verification-failure messages. Nil uses
	// slog.Default.
	Logger *slog.Logger
}

// LoadBundles reads every *.skill.json file under dir, parses
// + verifies each against opts.TrustedPublisherKeys, and
// returns the loaded skills. A missing dir is not an error —
// LoadBundles returns nil.
//
// Verification failures are always dropped (bundles are the
// signed-skills-only path; there's no "unverified but loaded"
// story). opts.Strict controls whether the failure log line
// is WARN (Strict=false) or ERROR (Strict=true).
func LoadBundles(dir string, opts BundleLoadOptions) ([]Skill, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: read bundles dir: %w", err)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".skill.json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := bundle.Load(path)
		if err != nil {
			logBundleFailure(logger, opts.Strict, "skills.bundle_parse_failed", path, err)
			continue
		}
		if err := b.Verify(opts.TrustedPublisherKeys); err != nil {
			logBundleFailure(logger, opts.Strict, "skills.bundle_verify_failed", path, err)
			continue
		}
		out = append(out, bundleToSkill(b, path))
	}
	return out, nil
}

// bundleToSkill adapts a verified bundle's manifest + content
// into the shipped [Skill] type so downstream callers don't
// need to know bundles from plain markdown.
func bundleToSkill(b *bundle.Bundle, path string) Skill {
	return Skill{
		Name:        b.Manifest.Name,
		Description: b.Manifest.Description,
		Triggers:    append([]string(nil), b.Manifest.Triggers...),
		Body:        b.Content,
		Path:        path,
	}
}

// logBundleFailure picks the log level (WARN vs ERROR) based
// on the strict flag. Content is otherwise identical so
// operators reading a SIEM feed always see the same fields.
func logBundleFailure(logger *slog.Logger, strict bool, event, path string, err error) {
	attrs := []any{
		slog.String("path", path),
		slog.String("err", err.Error()),
		slog.String("hint", "check the bundle's signature and the operator-configured trusted publishers list"),
	}
	if strict {
		logger.Error(event, attrs...)
	} else {
		logger.Warn(event, attrs...)
	}
}
