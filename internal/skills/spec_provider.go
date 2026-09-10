package skills

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// SpecProvider satisfies agent.SkillsProvider using the spec's
// three-tier progressive-disclosure model.
//
// Tier 1: SystemAppendix returns the `<available_skills>` catalog
// (name + description per skill, no bodies). Roughly 50-100 tokens
// per skill, so a 20-skill installation costs ~1500 tokens on
// every turn — vs the legacy Provider's habit of splicing every
// activated skill's full body into the prompt.
//
// Tier 2 + 3 (activation, resource resolution) happen through
// separate methods (Activate, ResolveResource) that the harness
// wires to a dedicated `activate_skill` tool or lets the model
// discover via file-read of the SKILL.md at the catalog's
// <location>. Both patterns are spec-legal — this Provider
// exposes both so callers can pick.
//
// SpecProvider is safe for concurrent use by construction: the
// underlying []SkillSpec is set at NewSpecProvider and never
// mutated. Rebuild the Provider (e.g. on operator reload) to pick
// up new skills.
type SpecProvider struct {
	skills []SkillSpec
	// byName pre-indexes for O(1) Activate lookups. Built once at
	// construction; nil-safe.
	byName map[string]*SkillSpec
}

// NewSpecProvider constructs a SpecProvider from an already-
// discovered list of SkillSpec. Nil / empty input is fine — the
// Provider then contributes nothing to system prompts and returns
// ErrSpecUnknownSkill from Activate.
func NewSpecProvider(discovered []SkillSpec) *SpecProvider {
	idx := make(map[string]*SkillSpec, len(discovered))
	for i := range discovered {
		s := &discovered[i]
		idx[s.Name] = s
	}
	return &SpecProvider{skills: discovered, byName: idx}
}

// NewSpecProviderFromDir discovers spec-compliant skills under
// root and wraps the result in a SpecProvider. A missing root is
// not an error — the returned Provider is a no-op. Invalid skills
// found in the root are silently skipped (spec-legal: a bad
// third-party skill must not wedge the daemon).
//
// Use rousseau skills validate (CLI, forthcoming) to surface
// per-skill parse / validation errors when authoring.
func NewSpecProviderFromDir(root string) (*SpecProvider, error) {
	discovered, err := DiscoverSpec(root, DiscoverOptions{})
	if err != nil {
		return nil, err
	}
	return NewSpecProvider(discovered), nil
}

// SystemAppendix satisfies agent.SkillsProvider by returning the
// tier-1 catalog block ready to append to a system prompt.
// Ignores the session — the spec's model-driven activation flow
// injects the catalog on every turn and lets the model decide.
func (p *SpecProvider) SystemAppendix(_ *agent.Session) string {
	if p == nil || len(p.skills) == 0 {
		return ""
	}
	return PromptBlock(p.skills)
}

// Skills returns the discovered skill list for CLI / doctor
// surfaces (`rousseau skills list`). Callers must treat the
// result as read-only — the Provider still owns the backing
// storage.
func (p *SpecProvider) Skills() []SkillSpec {
	if p == nil {
		return nil
	}
	return p.skills
}

// ErrSpecUnknownSkill is returned by Activate when the requested
// skill name is not in the Provider's catalog. Callers can use
// errors.Is to distinguish this from other failures.
var ErrSpecUnknownSkill = errSpecUnknownSkill{}

type errSpecUnknownSkill struct{}

func (errSpecUnknownSkill) Error() string {
	return "skills: no such skill in the discovered catalog"
}

// Activate returns the tier-2 body for a skill by name. The
// returned body is the raw SKILL.md content (frontmatter
// stripped) — callers wrap it in <skill_content name="..."> tags
// at their preferred wire format.
//
// The lookup is O(1) via the byName index built at construction.
func (p *SpecProvider) Activate(name string) (SkillSpec, error) {
	if p == nil || p.byName == nil {
		return SkillSpec{}, ErrSpecUnknownSkill
	}
	s, ok := p.byName[name]
	if !ok {
		return SkillSpec{}, ErrSpecUnknownSkill
	}
	return *s, nil
}

// ResolveResource is a convenience for the harness's tier-3 loader:
// given the activated skill's name and a relative path from its
// SKILL.md body, return the safe absolute path. Errors surface
// ErrResourceEscapesBase / ErrResourceAbsolutePath / ErrResourceEmpty
// per resource.go.
func (p *SpecProvider) ResolveResource(name, rel string) (string, error) {
	spec, err := p.Activate(name)
	if err != nil {
		return "", err
	}
	return ResolveResource(spec.BaseDir, rel)
}
