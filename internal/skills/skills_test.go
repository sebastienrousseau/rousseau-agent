package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func TestLoad_MissingDirIsNoError(t *testing.T) {
	got, err := Load("/nonexistent/path")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLoad_EmptyPathReturnsNil(t *testing.T) {
	got, err := Load("")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLoad_SkipsNonMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "keep.md", "hello world")
	writeSkill(t, dir, "skip.txt", "text")
	writeSkill(t, dir, "readme.MD", "capitalised suffix should be skipped")
	got, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "keep", got[0].Name)
}

func TestLoad_BareMarkdownDefaultsFromFilename(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "kubernetes.md", "This is the body.")
	got, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "kubernetes", got[0].Name)
	assert.Equal(t, "This is the body.", got[0].Body)
	assert.Empty(t, got[0].Triggers)
}

func TestLoad_ReadsFrontMatter(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "rebase.md", `---
name: git-rebase
description: Guide the user through a rebase.
triggers: [rebase, "git rebase", squash]
---
Body of the skill.`)
	got, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "git-rebase", got[0].Name)
	assert.Equal(t, "Guide the user through a rebase.", got[0].Description)
	assert.Equal(t, []string{"rebase", "git rebase", "squash"}, got[0].Triggers)
	assert.Equal(t, "Body of the skill.", got[0].Body)
}

func TestLoad_MalformedFrontMatterErrors(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "bad.md", "---\ntriggers: [not yaml at all\n---\nBody")
	_, err := Load(dir)
	assert.Error(t, err)
}

func TestSplitFrontMatter_NoHeader(t *testing.T) {
	fm, body := splitFrontMatter([]byte("just body"))
	assert.Empty(t, fm)
	assert.Equal(t, "just body", body)
}

func TestSplitFrontMatter_MissingClose(t *testing.T) {
	fm, body := splitFrontMatter([]byte("---\nno close"))
	assert.Empty(t, fm)
	assert.Equal(t, "---\nno close", body)
}

func TestSelect_ActivatesOnKeywordMatch(t *testing.T) {
	skills := []Skill{
		{Name: "git-rebase", Triggers: []string{"rebase", "squash"}},
		{Name: "kubernetes", Triggers: []string{"kubernetes", "kubectl"}},
	}
	got := Select(skills, "help me squash these commits")
	require.Len(t, got, 1)
	assert.Equal(t, "git-rebase", got[0].Name)
}

func TestSelect_CaseInsensitive(t *testing.T) {
	skills := []Skill{{Name: "kubernetes", Triggers: []string{"Kubernetes"}}}
	got := Select(skills, "how do I debug KUBERNETES pods")
	require.Len(t, got, 1)
}

func TestSelect_NoMatches(t *testing.T) {
	skills := []Skill{{Name: "kubernetes", Triggers: []string{"kubernetes"}}}
	got := Select(skills, "what is the weather today")
	assert.Empty(t, got)
}

func TestSelect_MultipleActivations(t *testing.T) {
	skills := []Skill{
		{Name: "git-rebase", Triggers: []string{"rebase"}},
		{Name: "kubernetes", Triggers: []string{"kubectl"}},
	}
	got := Select(skills, "I want to kubectl exec after a rebase")
	assert.Len(t, got, 2)
}

func TestSelect_EmptyTriggerIsIgnored(t *testing.T) {
	skills := []Skill{{Name: "never", Triggers: []string{""}}}
	got := Select(skills, "empty match")
	assert.Empty(t, got)
}

func TestSelect_DeduplicatesByName(t *testing.T) {
	skills := []Skill{
		{Name: "same", Triggers: []string{"a"}},
		{Name: "same", Triggers: []string{"b"}},
	}
	got := Select(skills, "a and b")
	assert.Len(t, got, 1)
}

func TestCompose_Empty(t *testing.T) {
	assert.Equal(t, "", Compose(nil))
}

func TestCompose_RendersSection(t *testing.T) {
	got := Compose([]Skill{
		{Name: "one", Description: "first", Body: "content one"},
		{Name: "two", Body: "content two"},
	})
	assert.Contains(t, got, "# Active skills")
	assert.Contains(t, got, "## one")
	assert.Contains(t, got, "*first*")
	assert.Contains(t, got, "content one")
	assert.Contains(t, got, "## two")
	assert.Contains(t, got, "content two")
}
