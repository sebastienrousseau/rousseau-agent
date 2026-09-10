package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for DiscoverSpec + findSkillFile. Locks in the
// spec's discovery contract (per-skill subdirectories with SKILL.md,
// depth cap, dir cap, skip-list, invalid-skill silence-vs-callback).

func writeSpecSkill(t *testing.T, dir, name, description, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	front := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(front), 0o600))
}

func TestDiscoverSpec_EmptyRootReturnsNil(t *testing.T) {
	skills, err := DiscoverSpec("", DiscoverOptions{})
	assert.NoError(t, err)
	assert.Nil(t, skills)
}

func TestDiscoverSpec_MissingRootReturnsNil(t *testing.T) {
	dir := t.TempDir()
	skills, err := DiscoverSpec(filepath.Join(dir, "no-such-dir"), DiscoverOptions{})
	assert.NoError(t, err)
	assert.Nil(t, skills)
}

func TestDiscoverSpec_HappyPath(t *testing.T) {
	root := t.TempDir()
	writeSpecSkill(t, filepath.Join(root, "alpha"), "alpha", "the alpha skill", "alpha body")
	writeSpecSkill(t, filepath.Join(root, "beta"), "beta", "the beta skill", "beta body")

	skills, err := DiscoverSpec(root, DiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, skills, 2)

	// Deterministic order (lexicographic via filepath.WalkDir).
	assert.Equal(t, "alpha", skills[0].Name)
	assert.Equal(t, "beta", skills[1].Name)
	assert.Equal(t, "alpha body", skills[0].Body)
	assert.Equal(t, filepath.Join(root, "alpha"), skills[0].BaseDir)
	assert.Equal(t, filepath.Join(root, "beta"), skills[1].BaseDir)
}

func TestDiscoverSpec_LowercaseSkillMDFallback(t *testing.T) {
	// Reference validator accepts skill.md as leniency; we do too.
	root := t.TempDir()
	dir := filepath.Join(root, "lower")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "skill.md"),
		[]byte("---\nname: lower\ndescription: x\n---\n"),
		0o600,
	))

	skills, err := DiscoverSpec(root, DiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "lower", skills[0].Name)
}

func TestDiscoverSpec_SkipsInvalidSkillsSilently(t *testing.T) {
	root := t.TempDir()
	// Valid.
	writeSpecSkill(t, filepath.Join(root, "good"), "good", "the good skill", "")
	// Invalid: name mismatch (dir=bad, name=different).
	writeSpecSkill(t, filepath.Join(root, "bad"), "different", "invalid skill", "")
	// Invalid: unknown frontmatter key.
	dir := filepath.Join(root, "typoed")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: typoed\ndescription: x\ntriggers: [foo]\n---\n"),
		0o600,
	))
	// Invalid: no frontmatter.
	dir2 := filepath.Join(root, "no-front")
	require.NoError(t, os.MkdirAll(dir2, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir2, "SKILL.md"),
		[]byte("just a plain markdown body\n"),
		0o600,
	))

	skills, err := DiscoverSpec(root, DiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, skills, 1, "only the valid skill should load")
	assert.Equal(t, "good", skills[0].Name)
}

func TestDiscoverSpec_OnInvalidCallbackReceivesAllFailures(t *testing.T) {
	root := t.TempDir()
	writeSpecSkill(t, filepath.Join(root, "good"), "good", "the good skill", "")
	writeSpecSkill(t, filepath.Join(root, "mismatch"), "wrong-name", "invalid", "")

	var got []string
	opts := DiscoverOptions{
		OnInvalid: func(path string, err error) {
			got = append(got, filepath.Base(filepath.Dir(path))+": "+err.Error())
		},
	}
	skills, err := DiscoverSpec(root, opts)
	require.NoError(t, err)
	assert.Len(t, skills, 1)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "mismatch:")
	assert.Contains(t, got[0], "does not match containing directory")
}

func TestDiscoverSpec_MaxDepthCap(t *testing.T) {
	root := t.TempDir()
	// Skill at depth 5 — reachable with default MaxDepth (6).
	depth5 := filepath.Join(root, "a", "b", "c", "d", "reachable")
	writeSpecSkill(t, depth5, "reachable", "at depth 5", "")
	// Skill at depth 8 — NOT reachable with default MaxDepth (6).
	depth8 := filepath.Join(root, "a", "b", "c", "d", "e", "f", "g", "unreachable")
	writeSpecSkill(t, depth8, "unreachable", "at depth 8", "")

	skills, err := DiscoverSpec(root, DiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "reachable", skills[0].Name)
}

func TestDiscoverSpec_MaxDepthOverride(t *testing.T) {
	root := t.TempDir()
	// Skill at depth 2 — reachable with MaxDepth=1? No, depth is 2.
	writeSpecSkill(t, filepath.Join(root, "a", "reachable"), "reachable", "at depth 2", "")

	skills, err := DiscoverSpec(root, DiscoverOptions{MaxDepth: 1})
	require.NoError(t, err)
	assert.Empty(t, skills, "depth-2 skill must be skipped when MaxDepth=1")
}

func TestDiscoverSpec_MaxDirsFails(t *testing.T) {
	root := t.TempDir()
	// Create more dirs than the cap.
	for i := 0; i < 20; i++ {
		require.NoError(t, os.MkdirAll(filepath.Join(root, strings.Repeat("d", i+1)), 0o755))
	}

	_, err := DiscoverSpec(root, DiscoverOptions{MaxDirs: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than 5 directories")
}

func TestDiscoverSpec_SkipList(t *testing.T) {
	root := t.TempDir()
	// A skill inside a skipped dir must not be discovered.
	writeSpecSkill(t, filepath.Join(root, ".git", "hidden"), "hidden", "in git", "")
	writeSpecSkill(t, filepath.Join(root, "node_modules", "hidden"), "hidden", "in node_modules", "")
	// A skill outside skipped dirs is discovered.
	writeSpecSkill(t, filepath.Join(root, "visible"), "visible", "at root", "")

	skills, err := DiscoverSpec(root, DiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "visible", skills[0].Name)
}

func TestDiscoverSpec_CustomSkipList(t *testing.T) {
	root := t.TempDir()
	writeSpecSkill(t, filepath.Join(root, ".git", "hidden"), "hidden", "in git", "")
	writeSpecSkill(t, filepath.Join(root, "visible"), "visible", "at root", "")

	// Empty skip list discovers everything, including .git.
	skills, err := DiscoverSpec(root, DiscoverOptions{Skip: map[string]struct{}{}})
	require.NoError(t, err)
	assert.Len(t, skills, 2, "with empty skip list, .git should be walked")
}

func TestDiscoverSpec_IgnoresSymlinkDirs(t *testing.T) {
	root := t.TempDir()
	writeSpecSkill(t, filepath.Join(root, "real"), "real", "the real skill", "")
	// A symlink to root itself would loop; skip should catch it.
	linkPath := filepath.Join(root, "loop")
	if err := os.Symlink(root, linkPath); err != nil {
		t.Skip("cannot create symlinks on this filesystem: " + err.Error())
	}

	skills, err := DiscoverSpec(root, DiscoverOptions{MaxDirs: 200})
	require.NoError(t, err)
	assert.Len(t, skills, 1, "the symlink loop must not be descended into")
}

// -- findSkillFile ----------------------------------------------------

func TestFindSkillFile_NeitherPresent(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, findSkillFile(dir))
}

func TestFindSkillFile_PrefersCanonical(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\ndescription: y\n---\n"), 0o600))
	got := findSkillFile(dir)
	assert.Equal(t, filepath.Join(dir, "SKILL.md"), got)
}

func TestFindSkillFile_IgnoresDirNamedSKILLmd(t *testing.T) {
	dir := t.TempDir()
	// A DIRECTORY called SKILL.md must not be mistaken for the file.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "SKILL.md"), 0o755))
	assert.Empty(t, findSkillFile(dir))
}
