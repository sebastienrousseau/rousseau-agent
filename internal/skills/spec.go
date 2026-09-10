// Spec-compliant Agent Skills — the three-tier progressive-disclosure
// model formalised at agentskills.io.
//
// The three tiers, exactly as the spec defines them:
//
//   1. Catalog — every discovered skill's name + description are
//      injected into the system prompt (~50-100 tokens per skill).
//      That is all the model sees until it decides to activate one.
//   2. Instructions — when the model activates a skill (by reading
//      SKILL.md itself or calling an activator tool), the SKILL.md
//      body is loaded (target < 5000 tokens).
//   3. Resources — files under scripts/, references/, assets/, or
//      anywhere relative to the skill directory, loaded one at a
//      time on the model's own Read / Bash tool calls.
//
// The old Skill / Load / Select / Compose flow in skills.go is the
// legacy "load-everything-into-system-prompt" model, retained for
// backwards compatibility while callers migrate. Everything in this
// file and its neighbours (discover.go, catalog.go, resource.go,
// spec_provider.go) is the spec-compliant replacement.

package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// SkillSpec is the six-field frontmatter shape defined by the Agent
// Skills spec. Every field except Name and Description is optional;
// the parser accepts any subset. Extra frontmatter keys are rejected
// so a typo (`descripton:`) never silently loads an unusable skill.
type SkillSpec struct {
	// Name identifies the skill. 1-64 chars, lowercase Unicode
	// alnum + hyphen; cannot start / end with `-`; no `--`.
	// Must (NFKC-normalised) equal the parent directory name.
	Name string
	// Description states both what the skill does AND when to use
	// it — the model reads only this text at tier 1, so imperative
	// phrasing ("Use this skill when...") is the recommended style.
	// 1-1024 chars.
	Description string
	// License is free-form. SPDX identifiers or a short pointer
	// ("Proprietary. See LICENSE.txt") are both spec-legal.
	License string
	// Compatibility documents environment requirements: intended
	// client, required binaries, network needs. ≤500 chars.
	Compatibility string
	// Metadata is a client-defined extension slot. Keys and values
	// are stringified. Recommended to namespace non-standard keys
	// (`x-rousseau-signature-id`, `x-rousseau-severity-scope`).
	Metadata map[string]string
	// AllowedTools is a space-separated tool-pattern hint,
	// experimental per the spec. Not a permission grant — the
	// harness's own permission system remains the source of truth.
	AllowedTools string

	// BaseDir is the absolute path to the skill's containing
	// directory (where SKILL.md lives). Filled by ReadSkill /
	// discovery; empty when the spec came from an in-memory
	// parse.
	BaseDir string
	// Body is the SKILL.md Markdown body with frontmatter stripped.
	// Loaded at parse time so tier-2 activation is a struct read,
	// not a file open — the spec permits either approach.
	Body string
}

// specFrontmatterFields is the closed set of frontmatter keys the
// spec permits. Anything outside this list makes the skill invalid.
// Kept as a map for O(1) lookup during validation.
var specFrontmatterFields = map[string]struct{}{
	"name":          {},
	"description":   {},
	"license":       {},
	"compatibility": {},
	"metadata":      {},
	"allowed-tools": {},
}

// nameRegexp matches the spec's naming rules: lowercase alnum + `-`,
// cannot start or end with `-`, no consecutive `--`. Length is
// bounded separately (1-64) so the regex stays cheap to read.
var nameRegexp = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Errors surfaced by ParseSkillMD / ValidateSpec so callers can
// classify without string matching.
var (
	// ErrSkillFrontmatterMissing means the input had no `---`-fenced
	// YAML block at the top — the spec requires frontmatter.
	ErrSkillFrontmatterMissing = errors.New("skills: SKILL.md missing frontmatter")
	// ErrSkillFrontmatterUnclosed means the opening `---` had no
	// matching close before end-of-file.
	ErrSkillFrontmatterUnclosed = errors.New("skills: SKILL.md frontmatter is not closed")
	// ErrSkillNameMissing means the frontmatter parsed but `name` is
	// empty; the spec's minimum-viable skill has name + description.
	ErrSkillNameMissing = errors.New("skills: SKILL.md frontmatter is missing required field `name`")
	// ErrSkillDescriptionMissing is the same for `description`.
	ErrSkillDescriptionMissing = errors.New("skills: SKILL.md frontmatter is missing required field `description`")
)

// ParseSkillMD splits raw SKILL.md bytes into a validated SkillSpec
// and the Markdown body. Returns a wrapping error identifying which
// spec rule was violated when the input is invalid.
//
// This is the ONLY entry point that produces a SkillSpec — callers
// that want validation should route through here rather than
// hand-constructing the struct.
func ParseSkillMD(raw []byte) (SkillSpec, error) {
	front, body, err := splitSpecFrontmatter(raw)
	if err != nil {
		return SkillSpec{}, err
	}

	// Parse into a generic map first so we can reject unknown keys
	// with a legible error (the spec's closed-key rule).
	var kv map[string]any
	if err := yaml.Unmarshal([]byte(front), &kv); err != nil {
		return SkillSpec{}, fmt.Errorf("skills: SKILL.md frontmatter parse: %w", err)
	}
	if kv == nil {
		return SkillSpec{}, ErrSkillFrontmatterMissing
	}
	for k := range kv {
		if _, ok := specFrontmatterFields[k]; !ok {
			return SkillSpec{}, fmt.Errorf(
				"skills: SKILL.md frontmatter has unknown field %q — allowed: name, description, license, compatibility, metadata, allowed-tools",
				k,
			)
		}
	}

	spec := SkillSpec{Body: strings.TrimSpace(body)}
	if v, ok := kv["name"]; ok {
		spec.Name = fmt.Sprint(v)
	}
	if v, ok := kv["description"]; ok {
		spec.Description = fmt.Sprint(v)
	}
	if v, ok := kv["license"]; ok {
		spec.License = fmt.Sprint(v)
	}
	if v, ok := kv["compatibility"]; ok {
		spec.Compatibility = fmt.Sprint(v)
	}
	if v, ok := kv["allowed-tools"]; ok {
		spec.AllowedTools = fmt.Sprint(v)
	}
	if raw, ok := kv["metadata"]; ok {
		m, err := coerceMetadata(raw)
		if err != nil {
			return SkillSpec{}, fmt.Errorf("skills: SKILL.md frontmatter metadata: %w", err)
		}
		spec.Metadata = m
	}
	return spec, nil
}

// ValidateSpec applies the spec's constraint checks to a parsed
// SkillSpec. Returns nil when the spec is loadable, otherwise a
// wrapping error naming the specific rule that failed.
//
// dirName is the parent directory name (base of BaseDir); when
// non-empty the validator enforces the NFKC name-vs-directory rule.
// Pass "" to skip the dir check (useful for in-memory tests).
func ValidateSpec(spec SkillSpec, dirName string) error {
	if spec.Name == "" {
		return ErrSkillNameMissing
	}
	if utf8.RuneCountInString(spec.Name) > 64 {
		return fmt.Errorf("skills: name %q exceeds 64 characters", spec.Name)
	}
	if !nameRegexp.MatchString(spec.Name) {
		return fmt.Errorf(
			"skills: name %q must be lowercase alnum + `-` only, cannot start / end with `-` or contain `--`",
			spec.Name,
		)
	}
	if dirName != "" {
		if norm.NFKC.String(spec.Name) != norm.NFKC.String(dirName) {
			return fmt.Errorf(
				"skills: name %q does not match containing directory %q (case-, unicode-, and normalisation-sensitive per spec)",
				spec.Name, dirName,
			)
		}
	}
	if spec.Description == "" {
		return ErrSkillDescriptionMissing
	}
	if utf8.RuneCountInString(spec.Description) > 1024 {
		return fmt.Errorf(
			"skills: description exceeds 1024 characters (was %d)",
			utf8.RuneCountInString(spec.Description),
		)
	}
	if utf8.RuneCountInString(spec.Compatibility) > 500 {
		return fmt.Errorf(
			"skills: compatibility exceeds 500 characters (was %d)",
			utf8.RuneCountInString(spec.Compatibility),
		)
	}
	return nil
}

// splitSpecFrontmatter separates a `---\n...\n---` YAML block at the
// top of the file from the body. Returns ErrSkillFrontmatterMissing
// when there is no opening `---` and ErrSkillFrontmatterUnclosed when
// the opening `---` has no matching close.
//
// Distinct from the legacy splitFrontMatter helper in skills.go: the
// spec REQUIRES frontmatter, so a missing block is an error here
// (legacy returns "", raw and lets the caller continue).
func splitSpecFrontmatter(raw []byte) (front, body string, err error) {
	s := string(raw)
	// Tolerate CRLF and a leading UTF-8 BOM (U+FEFF) — common on
	// Windows-authored or copy-pasted skill files.
	s = strings.TrimPrefix(s, "\ufeff")
	switch {
	case strings.HasPrefix(s, "---\n"):
		s = strings.TrimPrefix(s, "---\n")
	case strings.HasPrefix(s, "---\r\n"):
		s = strings.TrimPrefix(s, "---\r\n")
	default:
		return "", "", ErrSkillFrontmatterMissing
	}
	// Look for the closing `---` on its own line.
	end := indexClose(s)
	if end < 0 {
		return "", "", ErrSkillFrontmatterUnclosed
	}
	front = s[:end]
	rest := s[end:]
	// Strip the closing marker (`\n---` or `\r\n---`) and any
	// trailing newline before the body starts.
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	rest = strings.TrimPrefix(rest, "---")
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	return front, rest, nil
}

// indexClose locates the first `\n---` (or `\r\n---`) line that
// closes the frontmatter block. Returns -1 when no close is found.
func indexClose(s string) int {
	// Match `\n---` followed by EOL or EOF so `---plus-more` in the
	// body doesn't accidentally close the block.
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], "\n---")
		if j < 0 {
			return -1
		}
		abs := i + j
		after := abs + len("\n---")
		// End-of-file or newline immediately after the `---` marker
		// = valid close. Anything else = false positive, keep looking.
		if after == len(s) || s[after] == '\n' || s[after] == '\r' {
			return abs
		}
		i = after
	}
	return -1
}

// coerceMetadata takes the raw yaml-parsed metadata value (which
// could be map[string]any, map[any]any, or nil) and forces both
// keys and values into strings so the returned map matches the
// spec's `map[string]string` requirement without leaking yaml-node
// types up the call stack.
func coerceMetadata(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	out := map[string]string{}
	switch m := raw.(type) {
	case map[string]any:
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
	case map[any]any:
		for k, v := range m {
			out[fmt.Sprint(k)] = fmt.Sprint(v)
		}
	default:
		return nil, fmt.Errorf("metadata must be a mapping, got %T", raw)
	}
	return out, nil
}
