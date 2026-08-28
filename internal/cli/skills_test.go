package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// withSystemSkillsDir repoints the system-wide skills bundle at an
// empty directory for the duration of the test and restores the
// previous value on cleanup.
//
// Without this, skills tests are not hermetic. The container image
// populates /etc/rousseau/skills with the bundled starter skills, so
// loadSkillsFromResolutionChain overlays them onto whatever the test
// put in its own TempDir. A test asserting "(no skills)" then passes
// on a bare workstation and fails inside the image — exactly the kind
// of environment-dependent failure that is expensive to diagnose in
// CI.
func withSystemSkillsDir(t *testing.T, dir string) {
	t.Helper()
	prev := systemSkillsDir
	systemSkillsDir = dir
	t.Cleanup(func() { systemSkillsDir = prev })
}

func makeSkillsOpts(t *testing.T) *Options {
	t.Helper()
	dir := t.TempDir()
	// Isolate from the system bundle so results depend only on `dir`.
	withSystemSkillsDir(t, t.TempDir())
	return &Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: dir}},
		Logger: silentLogger(),
	}
}

func TestResolveSkillsDir_ExplicitConfig(t *testing.T) {
	opts := &Options{Config: &config.Config{Agent: config.AgentConfig{SkillsDir: "/opt/skills"}}}
	assert.Equal(t, "/opt/skills", resolveSkillsDir(opts))
}

func TestResolveSkillsDir_DefaultsToHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	opts := &Options{Config: &config.Config{}}
	got := resolveSkillsDir(opts)
	assert.Contains(t, got, "rousseau/skills")
}

func TestSkillsListCmd_Empty(t *testing.T) {
	opts := makeSkillsOpts(t)
	cmd := newSkillsListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "no skills")
}

func TestSkillsListCmd_ReturnsSkills(t *testing.T) {
	opts := makeSkillsOpts(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(opts.Config.Agent.SkillsDir, "one.md"),
		[]byte(`---
name: one
description: first
triggers: [foo]
---
Body one.`), 0o644))
	cmd := newSkillsListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "one")
	assert.Contains(t, buf.String(), "first")
}

func TestSkillsShowCmd_MissingSkill(t *testing.T) {
	opts := makeSkillsOpts(t)
	cmd := newSkillsShowCmd(opts)
	err := cmd.RunE(cmd, []string{"missing"})
	assert.Error(t, err)
}

func TestSkillsShowCmd_PrintsBody(t *testing.T) {
	opts := makeSkillsOpts(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(opts.Config.Agent.SkillsDir, "one.md"),
		[]byte("Body content"), 0o644))
	cmd := newSkillsShowCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.RunE(cmd, []string{"one"}))
	assert.Contains(t, buf.String(), "Body content")
}

func TestNewSkillsCmd_HasSubcommands(t *testing.T) {
	cmd := newSkillsCmd(&Options{Config: &config.Config{}})
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["list"])
	assert.True(t, names["show"])
}

func TestBuildSkillsProvider_ReturnsProvider(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{Config: &config.Config{Agent: config.AgentConfig{SkillsDir: dir}}}
	p, err := buildSkillsProvider(opts)
	require.NoError(t, err)
	assert.NotNil(t, p)
}

// writeSkill drops a minimal well-formed skill file into dir.
func writeSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	body := "---\nname: " + name + "\ndescription: " + desc + "\ntriggers: [" + name + "]\n---\nBody for " + name + "."
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644))
}

// skillNames extracts the Name field from a slice of skills so tests
// can assert on set membership without depending on ordering.
func skillNames(in []skills.Skill) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Name)
	}
	return out
}

func TestLoadSkillsFromResolutionChain_OverlaysSystemBundle(t *testing.T) {
	userDir, sysDir := t.TempDir(), t.TempDir()
	withSystemSkillsDir(t, sysDir)
	writeSkill(t, userDir, "user-only", "from user dir")
	writeSkill(t, sysDir, "system-only", "from system bundle")

	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
		Logger: silentLogger(),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user-only", "system-only"}, skillNames(got))
}

func TestLoadSkillsFromResolutionChain_UserShadowsSystem(t *testing.T) {
	userDir, sysDir := t.TempDir(), t.TempDir()
	withSystemSkillsDir(t, sysDir)
	writeSkill(t, userDir, "shared", "user wins")
	writeSkill(t, sysDir, "shared", "system loses")

	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
		Logger: silentLogger(),
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "same-named system skill must not duplicate the user one")
	assert.Equal(t, "shared", got[0].Name)
	assert.Equal(t, "user wins", got[0].Description)
}

func TestLoadSkillsFromResolutionChain_MissingSystemDirIsNotFatal(t *testing.T) {
	userDir := t.TempDir()
	// Point the system bundle at a path that does not exist.
	withSystemSkillsDir(t, filepath.Join(t.TempDir(), "absent"))
	writeSkill(t, userDir, "user-only", "from user dir")

	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
		Logger: silentLogger(),
	})
	require.NoError(t, err, "an unreadable system bundle must degrade, not fail")
	assert.Equal(t, []string{"user-only"}, skillNames(got))
}

func TestLoadSkillsFromResolutionChain_EmptySystemDirReturnsPrimary(t *testing.T) {
	userDir := t.TempDir()
	withSystemSkillsDir(t, t.TempDir()) // exists but contains no skills
	writeSkill(t, userDir, "user-only", "from user dir")

	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
		Logger: silentLogger(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-only"}, skillNames(got))
}

func TestBuildRecallProvider_NilStoreReturnsNil(t *testing.T) {
	assert.Nil(t, buildRecallProvider(nil))
}

func TestBuildRecallProvider_ReturnsWrapper(t *testing.T) {
	s, err := sqlitestore.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	got := buildRecallProvider(s)
	assert.NotNil(t, got)
}
