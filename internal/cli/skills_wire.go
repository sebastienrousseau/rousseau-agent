package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

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
// The loader branches on cfg.Agent.SkillsMode:
//
//   - "" or "legacy" (default) — the pre-Phase-2.1 flat-file model.
//     SkillsDir is scanned non-recursively for *.md; each file's
//     triggers: keyword list drives activation; every activated
//     body is spliced into the system prompt. Signed bundles
//     (agent.skill_bundles) append to this set when licensed.
//   - "spec" — the agentskills.io three-tier progressive-disclosure
//     model (see internal/skills/spec.go). SkillsDir is walked for
//     per-skill subdirectories containing SKILL.md; only the tier-1
//     catalog (name + description) is injected into the system
//     prompt; bodies are read by the model on demand.
//
// Bundle-mode skills are legacy-only for now: a spec-mode config
// with skill_bundles.dir set logs a single WARN pointing to the
// planned x-rousseau-signature verification path and continues
// without loading the bundles.
//
// Returns (nil, nil) when NEITHER source is configured, matching
// the pre-Phase-2.1 caller contract that a nil provider means
// "no skills at all" — the daemon wiring uses this to decide
// whether to plumb a SkillsProvider through agent.Options.
func buildSkillsProvider(opts *Options, checker license.Checker) (agent.SkillsProvider, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dir := resolveSkillsDir(opts)
	mode := skillsMode(opts.Config.Agent.SkillsMode)
	bundleCfg := opts.Config.Agent.SkillBundles

	// No source configured at all — return (nil, nil) so the daemon
	// wiring skips the SkillsProvider option entirely.
	if dir == "" && bundleCfg.Dir == "" {
		return nil, nil
	}

	if mode == skillsModeSpec {
		return buildSpecSkillsProvider(dir, bundleCfg, logger)
	}
	return buildLegacySkillsProvider(dir, bundleCfg, checker, logger)
}

// skillsMode enum keeps the string-comparison in one place so any
// future value ("spec-v2", "hybrid") stays typo-proof at the
// callsite.
type skillsModeValue int

const (
	skillsModeLegacy skillsModeValue = iota
	skillsModeSpec
)

// skillsMode normalises the operator's config string. Empty and
// "legacy" both map to the legacy loader — the same three-state
// contract every other rousseau enum uses ("" ≡ default).
func skillsMode(s string) skillsModeValue {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "spec":
		return skillsModeSpec
	default:
		return skillsModeLegacy
	}
}

// buildLegacySkillsProvider preserves the pre-Phase-2.1 flat-file
// behaviour: skills.Load reads *.md non-recursively, signed
// bundles append when licensed. Extracted so the branching in
// buildSkillsProvider stays legible.
func buildLegacySkillsProvider(dir string, bundleCfg config.SkillBundlesConfig, checker license.Checker, logger *slog.Logger) (agent.SkillsProvider, error) {
	var plain []skills.Skill
	if dir != "" {
		loaded, err := skills.Load(dir)
		if err != nil {
			return nil, err
		}
		plain = loaded
	}

	bundles, err := loadSignedBundlesIfLicensed(bundleCfg, checker, logger)
	if err != nil {
		return nil, err
	}

	combined := make([]skills.Skill, 0, len(plain)+len(bundles))
	combined = append(combined, plain...)
	combined = append(combined, bundles...)
	return skills.NewProvider(combined), nil
}

// buildSpecSkillsProvider drives the agentskills.io three-tier
// loader against SkillsDir. Signed-bundle configuration is
// currently a legacy-only concept; when the operator sets it in
// spec mode, log a single WARN pointing to the planned
// metadata.x-rousseau-signature verification path and continue
// without bundles rather than silently ignoring them.
func buildSpecSkillsProvider(dir string, bundleCfg config.SkillBundlesConfig, logger *slog.Logger) (agent.SkillsProvider, error) {
	if bundleCfg.Dir != "" {
		logger.Warn("skills.spec_ignores_bundles",
			slog.String("bundles_dir", bundleCfg.Dir),
			slog.String("hint", "signed spec-mode skills use metadata.x-rousseau-signature (planned); the legacy skill_bundles path is not consulted in spec mode"),
		)
	}
	if dir == "" {
		// No SkillsDir but SkillBundles was set — spec mode has
		// nothing to load. Return an empty spec provider so the
		// system-prompt catalog stays valid (empty <available_skills>
		// = no injection at all per Catalog's contract).
		return skills.NewSpecProvider(nil), nil
	}
	provider, err := skills.NewSpecProviderFromDir(dir)
	if err != nil {
		return nil, err
	}
	logger.Info("skills.spec_mode",
		slog.String("dir", dir),
		slog.Int("discovered", len(provider.Skills())),
	)
	return provider, nil
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

// buildRecallProvider constructs an FTS-backed recall provider
// from a SearchableStore, skipping the current session's own
// snippets. Both drivers satisfy SearchableStore so this stays
// driver-agnostic — the sqlite adapter's Search / Postgres
// tsvector Search are called through the same interface.
func buildRecallProvider(store SearchableStore) agent.RecallProvider {
	if store == nil {
		return nil
	}
	return &agent.FTSRecall{
		Searcher:      &searchableRecall{store: store},
		SkipSessionID: func(s *agent.Session) string { return s.ID },
	}
}

// searchableRecall adapts a SearchableStore to
// [agent.RecallSearcher]. Converts sqlite.SearchHit (the shared
// domain type; postgres re-exports it as a type alias) into
// agent.SearchHit so the agent package stays independent of
// storage.
type searchableRecall struct {
	store SearchableStore
}

// Search satisfies [agent.RecallSearcher].
func (r *searchableRecall) Search(ctx context.Context, query string, limit int) ([]agent.SearchHit, error) {
	hits, err := r.store.Search(ctx, query, sqlitestore.SearchOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]agent.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.SearchHit{SessionID: h.SessionID, Title: h.Title, Snippet: h.Snippet})
	}
	return out, nil
}
