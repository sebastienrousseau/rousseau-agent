package skills

import (
	"bytes"
	"encoding/xml"
	"sort"
)

// Catalog renders a slice of SkillSpec as the tier-1
// `<available_skills>` XML block that gets injected into the model's
// system prompt.
//
// Format (matches the reference `skills-ref` Python library emit so
// cross-client behaviour is stable — a skill authored to trigger
// against Claude Code triggers the same way here):
//
//	<available_skills>
//	  <skill>
//	    <name>skill-name</name>
//	    <description>What the skill does AND when to use it.</description>
//	    <location>/abs/path/to/skill-dir</location>
//	  </skill>
//	  ...
//	</available_skills>
//
// The model reads name + description at every turn; when a task
// matches, it activates the skill by reading location or calling
// the activator tool.
//
// Callers should append the returned string to a base system
// prompt with a short preamble (see PromptPreamble) telling the
// model how to activate. Empty input returns "" so the base prompt
// stays unchanged.
func Catalog(skills []SkillSpec) string {
	if len(skills) == 0 {
		return ""
	}
	// Sort by name so the emitted catalog is deterministic across
	// runs — protects prompt caching (Anthropic's cache-breakpoint
	// hashing) from being invalidated by unstable iteration order.
	sorted := append([]SkillSpec(nil), skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var buf bytes.Buffer
	buf.WriteString("<available_skills>\n")
	for _, s := range sorted {
		buf.WriteString("  <skill>\n    <name>")
		xmlEscape(&buf, s.Name)
		buf.WriteString("</name>\n    <description>")
		xmlEscape(&buf, s.Description)
		buf.WriteString("</description>\n")
		if s.BaseDir != "" {
			buf.WriteString("    <location>")
			xmlEscape(&buf, s.BaseDir)
			buf.WriteString("</location>\n")
		}
		buf.WriteString("  </skill>\n")
	}
	buf.WriteString("</available_skills>")
	return buf.String()
}

// PromptPreamble is the recommended natural-language instruction to
// prepend the Catalog output with. Explains to the model how to
// activate a skill and reminds it that the catalog is exhaustive
// (so it does not invent skill names).
//
// Callers can substitute their own preamble; this default matches
// the spec's suggested phrasing so a model familiar with any
// spec-compliant client understands the same instructions.
const PromptPreamble = `You have access to the skills listed below. Each skill's <description> states what it does and when to use it. When a user task matches a skill's description, use your Read tool on the skill's <location> path (which points to a SKILL.md file) to load the full instructions. Only activate skills that clearly apply — do not invent skill names.`

// PromptBlock combines PromptPreamble with the rendered Catalog,
// separated by a blank line, so callers can add a single string to
// the system prompt. Returns "" when the catalog is empty (no
// skills → no preamble either).
func PromptBlock(skills []SkillSpec) string {
	cat := Catalog(skills)
	if cat == "" {
		return ""
	}
	return PromptPreamble + "\n\n" + cat
}

// xmlEscape writes the XML-safe form of s to buf. Uses
// encoding/xml's helper so `&`, `<`, `>`, `'`, `"` and control
// chars are all handled correctly — a hand-rolled escape would
// miss the numeric-character-reference cases the spec requires.
func xmlEscape(buf *bytes.Buffer, s string) {
	if s == "" {
		return
	}
	// EscapeText writes to any io.Writer; bytes.Buffer implements
	// it. Errors from bytes.Buffer.Write are impossible per the
	// stdlib contract, so ignoring the return value is safe.
	_ = xml.EscapeText(buf, []byte(s)) //nolint:errcheck // bytes.Buffer.Write cannot fail per stdlib contract
}
