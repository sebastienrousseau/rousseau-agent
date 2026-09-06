package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for SpecProvider — the tier-1 / tier-2 / tier-3
// integration surface that the agent daemon plugs into.

func TestNewSpecProvider_NilInputIsNoOp(t *testing.T) {
	p := NewSpecProvider(nil)
	require.NotNil(t, p)
	assert.Empty(t, p.SystemAppendix(nil))
	assert.Nil(t, p.Skills())
	_, err := p.Activate("anything")
	assert.ErrorIs(t, err, ErrSpecUnknownSkill)
}

func TestNewSpecProvider_EmptyInputIsNoOp(t *testing.T) {
	p := NewSpecProvider([]SkillSpec{})
	assert.Empty(t, p.SystemAppendix(nil))
	_, err := p.Activate("anything")
	assert.ErrorIs(t, err, ErrSpecUnknownSkill)
}

func TestSpecProvider_SystemAppendixEmitsCatalog(t *testing.T) {
	p := NewSpecProvider([]SkillSpec{
		{Name: "alpha", Description: "the alpha skill", BaseDir: "/skills/alpha", Body: "alpha body"},
		{Name: "beta", Description: "the beta skill", BaseDir: "/skills/beta", Body: "beta body"},
	})
	got := p.SystemAppendix(nil)
	assert.Contains(t, got, PromptPreamble)
	assert.Contains(t, got, "<name>alpha</name>")
	assert.Contains(t, got, "<name>beta</name>")
	// Bodies MUST NOT leak into tier-1 — the whole progressive-
	// disclosure win depends on this.
	assert.NotContains(t, got, "alpha body")
	assert.NotContains(t, got, "beta body")
}

func TestSpecProvider_NilProviderSurvives(t *testing.T) {
	var p *SpecProvider
	assert.Empty(t, p.SystemAppendix(nil))
	assert.Nil(t, p.Skills())
	_, err := p.Activate("x")
	assert.ErrorIs(t, err, ErrSpecUnknownSkill)
}

func TestSpecProvider_Activate(t *testing.T) {
	p := NewSpecProvider([]SkillSpec{
		{Name: "known", Description: "d", Body: "the body", BaseDir: "/x"},
	})
	spec, err := p.Activate("known")
	require.NoError(t, err)
	assert.Equal(t, "the body", spec.Body)
	assert.Equal(t, "/x", spec.BaseDir)

	_, err = p.Activate("unknown")
	assert.ErrorIs(t, err, ErrSpecUnknownSkill)
	assert.Contains(t, err.Error(), "no such skill")
}

func TestNewSpecProviderFromDir_HappyPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hello")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: hello\ndescription: greet the user\n---\nHi!\n"),
		0o600,
	))

	p, err := NewSpecProviderFromDir(root)
	require.NoError(t, err)
	require.NotNil(t, p)
	skills := p.Skills()
	require.Len(t, skills, 1)
	assert.Equal(t, "hello", skills[0].Name)
	assert.Equal(t, "greet the user", skills[0].Description)
	assert.Equal(t, "Hi!", skills[0].Body)
}

func TestNewSpecProviderFromDir_MissingDirIsNoOp(t *testing.T) {
	p, err := NewSpecProviderFromDir("/definitely/does/not/exist/rousseau-tests")
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Empty(t, p.Skills())
}

func TestSpecProvider_ResolveResource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run")
	scripts := filepath.Join(dir, "scripts")
	require.NoError(t, os.MkdirAll(scripts, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: run\ndescription: exec a script\n---\nSee scripts/x.sh\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(filepath.Join(scripts, "x.sh"), []byte("#!/bin/sh\n"), 0o600))

	p, err := NewSpecProviderFromDir(root)
	require.NoError(t, err)

	// Legitimate relative-path resolution succeeds and returns an
	// absolute path inside the skill base.
	got, err := p.ResolveResource("run", "scripts/x.sh")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got))

	// Directory traversal is refused via the underlying resource
	// safety check.
	_, err = p.ResolveResource("run", "../../../etc/passwd")
	assert.ErrorIs(t, err, ErrResourceEscapesBase)

	// Unknown skill surfaces ErrSpecUnknownSkill, not a filesystem
	// error — otherwise an attacker could probe skill names by
	// varying the resource path.
	_, err = p.ResolveResource("nonexistent", "x.sh")
	assert.ErrorIs(t, err, ErrSpecUnknownSkill)
}
