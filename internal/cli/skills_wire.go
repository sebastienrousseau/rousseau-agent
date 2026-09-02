package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// buildSkillsProvider loads skills from disk (empty dir is fine — the
// returned provider becomes a no-op) and adapts them to the agent's
// SkillsProvider seam.
//
// Two sources compose:
//
//   - Plain markdown skills (agent.skills_dir) — the OSS default;
//     loaded unconditionally, no licence gate. Optional SSH-based
//     signature verification via [skills.LoadVerified] follows a
//     separate config path (unchanged by this PR).
//   - Signed bundles (agent.skill_bundles.dir) — enterprise-only;
//     loaded when checker unlocks [license.FeatureGovernanceAdvanced]
//     AND the operator supplied trusted publisher keys. Verified
//     bundles append to the plain-markdown set; unverified bundles
//     are silently dropped (WARN or ERROR log per Strict flag).
//
// A missing licence → bundles ignored with a single INFO log so
// operators see "you configured signed bundles but they're inert".
func buildSkillsProvider(opts *Options, checker license.Checker) (agent.SkillsProvider, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dir := resolveSkillsDir(opts)
	var plain []skills.Skill
	if dir != "" {
		loaded, err := skills.Load(dir)
		if err != nil {
			return nil, err
		}
		plain = loaded
	}

	bundleCfg := opts.Config.Agent.SkillBundles
	bundles, err := loadSignedBundlesIfLicensed(bundleCfg, checker, logger)
	if err != nil {
		return nil, err
	}

	// Return a nil provider ONLY when no source was
	// configured — matches the pre-bundles behaviour where a
	// configured-but-empty skills_dir still produced a
	// (no-op) Provider. Callers rely on this distinction
	// when deciding whether to plumb the SkillsProvider
	// through agent.Options.
	if dir == "" && bundleCfg.Dir == "" {
		return nil, nil
	}
	combined := make([]skills.Skill, 0, len(plain)+len(bundles))
	combined = append(combined, plain...)
	combined = append(combined, bundles...)
	return skills.NewProvider(combined), nil
}

// loadSignedBundlesIfLicensed returns verified signed skills or
// nil. Reads the SkillBundlesConfig + licence and applies the
// same three-condition gate as the governance wrappers:
//
//   - No bundles dir configured → return nil (no noise).
//   - Configured but licence doesn't unlock → INFO log +
//     return nil (operator sees "your bundles are inert").
//   - Configured + licensed but trust list empty → WARN log
//   - return nil (a bundle-dir without trusted keys is
//     meaningless; refuse to silently ignore signatures).
func loadSignedBundlesIfLicensed(cfg config.SkillBundlesConfig, checker license.Checker, logger *slog.Logger) ([]skills.Skill, error) {
	if cfg.Dir == "" {
		return nil, nil
	}
	if checker == nil || !checker.IsEnabled(license.FeatureGovernanceAdvanced) {
		logger.Info("skills.bundles.licence_required",
			slog.String("dir", cfg.Dir),
			slog.String("feature", string(license.FeatureGovernanceAdvanced)),
			slog.String("hint", "add ROUSSEAU_LICENSE_KEY with governance_advanced to activate; see docs/COMMERCIAL.md"),
		)
		return nil, nil
	}
	trusted, err := decodeTrustedPublisherKeys(cfg.TrustedPublisherKeys)
	if err != nil {
		logger.Warn("skills.bundles.trust_list_invalid",
			slog.String("err", err.Error()),
			slog.String("hint", "each entry must be base64 std-encoded Ed25519 public key (32 bytes decoded)"),
		)
		return nil, nil
	}
	if len(trusted) == 0 {
		logger.Warn("skills.bundles.no_trusted_publishers",
			slog.String("dir", cfg.Dir),
			slog.String("hint", "add publisher keys to agent.skill_bundles.trusted_publisher_keys — refusing to load unsigned-by-anyone-we-trust bundles"),
		)
		return nil, nil
	}
	return skills.LoadBundles(cfg.Dir, skills.BundleLoadOptions{
		TrustedPublisherKeys: trusted,
		Strict:               cfg.Strict,
		Logger:               logger,
	})
}

// decodeTrustedPublisherKeys turns the operator's base64
// strings into ed25519.PublicKey values, skipping empty
// entries (whitespace-only YAML noise). Returns the first
// decode error — a mis-typed key is worth flagging loudly.
func decodeTrustedPublisherKeys(encoded []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(encoded))
	for i, s := range encoded {
		if s == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("entry %d: decoded key is %d bytes, want %d", i, len(raw), ed25519.PublicKeySize)
		}
		out = append(out, raw)
	}
	return out, nil
}

// buildRecallProvider constructs an FTS-backed recall provider from
// the sqlite store, skipping the current session's own snippets.
func buildRecallProvider(store *sqlitestore.Store) agent.RecallProvider {
	if store == nil {
		return nil
	}
	return &agent.FTSRecall{
		Searcher:      sqlitestore.NewRecallSearcher(store),
		SkipSessionID: func(s *agent.Session) string { return s.ID },
	}
}
