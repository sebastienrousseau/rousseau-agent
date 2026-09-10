package skills

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for Catalog + PromptBlock. Locks in the tier-1
// wire format (deterministic sort, XML escaping, location field
// omitted when BaseDir is empty).

func TestCatalog_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Empty(t, Catalog(nil))
	assert.Empty(t, Catalog([]SkillSpec{}))
}

func TestCatalog_SingleSkill(t *testing.T) {
	got := Catalog([]SkillSpec{
		{Name: "git-rebase", Description: "Guide the user through interactive rebase safely.", BaseDir: "/skills/git-rebase"},
	})
	assert.Equal(t, `<available_skills>
  <skill>
    <name>git-rebase</name>
    <description>Guide the user through interactive rebase safely.</description>
    <location>/skills/git-rebase</location>
  </skill>
</available_skills>`, got)
}

func TestCatalog_SortsAlphabeticallyForCacheStability(t *testing.T) {
	// Prompt caching depends on deterministic input hashing. Catalog
	// output MUST sort by name regardless of the input order or the
	// filesystem's walk order.
	got := Catalog([]SkillSpec{
		{Name: "zebra", Description: "z"},
		{Name: "alpha", Description: "a"},
		{Name: "mango", Description: "m"},
	})
	// alpha, mango, zebra
	idxAlpha := strings.Index(got, "<name>alpha</name>")
	idxMango := strings.Index(got, "<name>mango</name>")
	idxZebra := strings.Index(got, "<name>zebra</name>")
	require.NotEqual(t, -1, idxAlpha)
	require.NotEqual(t, -1, idxMango)
	require.NotEqual(t, -1, idxZebra)
	assert.Less(t, idxAlpha, idxMango)
	assert.Less(t, idxMango, idxZebra)
}

func TestCatalog_OmitsLocationWhenBaseDirEmpty(t *testing.T) {
	// In-memory specs (from ParseSkillMD without discovery) have
	// no BaseDir. The catalog must skip the <location> element
	// entirely rather than emit an empty tag.
	got := Catalog([]SkillSpec{
		{Name: "x", Description: "y"},
	})
	assert.Contains(t, got, "<name>x</name>")
	assert.NotContains(t, got, "<location>")
	assert.NotContains(t, got, "<location/>")
}

func TestCatalog_XMLEscapesSpecialChars(t *testing.T) {
	// Description with & < > ' " must be entity-encoded so a
	// hostile description cannot break out of the XML container
	// or the surrounding system prompt.
	got := Catalog([]SkillSpec{
		{Name: "ok", Description: "uses <foo> and & things", BaseDir: "/a"},
	})
	assert.NotContains(t, got, "<foo>", "raw < must be escaped")
	assert.NotContains(t, got, " & ", "raw & must be escaped")
	assert.Contains(t, got, "&lt;foo&gt;")
	assert.Contains(t, got, "&amp;")
}

func TestCatalog_XMLEscapesInLocation(t *testing.T) {
	// Locations with characters that would confuse an XML parser
	// (unlikely on real paths but still) must be escaped.
	got := Catalog([]SkillSpec{
		{Name: "n", Description: "d", BaseDir: "/path/with & ampersand"},
	})
	assert.Contains(t, got, "&amp;")
	assert.NotContains(t, got, "/path/with & ampersand</location>")
}

// -- PromptBlock ------------------------------------------------------

func TestPromptBlock_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Empty(t, PromptBlock(nil))
	assert.Empty(t, PromptBlock([]SkillSpec{}))
}

func TestPromptBlock_ContainsPreambleAndCatalog(t *testing.T) {
	got := PromptBlock([]SkillSpec{{Name: "n", Description: "d"}})
	assert.Contains(t, got, PromptPreamble)
	assert.Contains(t, got, "<available_skills>")
	// Preamble comes BEFORE the catalog.
	assert.Less(t, strings.Index(got, PromptPreamble), strings.Index(got, "<available_skills>"))
}
