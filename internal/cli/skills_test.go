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
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func makeSkillsOpts(t *testing.T) *Options {
	t.Helper()
	dir := t.TempDir()
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

func TestBuildRecallProvider_NilStoreReturnsNil(t *testing.T) {
	assert.Nil(t, buildRecallProvider(nil))
}

func TestBuildRecallProvider_ReturnsWrapper(t *testing.T) {
	s, err := sqlitestore.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	got := buildRecallProvider(s)
	assert.NotNil(t, got)
}
