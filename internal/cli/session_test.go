package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func TestShortID(t *testing.T) {
	assert.Equal(t, "hi", shortID("hi"))
	assert.Equal(t, "abcdefgh", shortID("abcdefghijklmnop"))
}

func makeOpts(t *testing.T) *Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	// Prime the file so openStore does not fight over creation.
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return &Options{
		Config: &config.Config{State: config.StateConfig{Path: path}},
		Logger: silentLogger(),
	}
}

func TestSessionListCmd_Empty(t *testing.T) {
	opts := makeOpts(t)
	cmd := newSessionListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "no sessions")
}

func TestSessionListCmd_ReturnsRows(t *testing.T) {
	opts := makeOpts(t)
	// Pre-populate the store.
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	sess := agent.NewSession("hello")
	sess.Append(agent.NewUserText("hi"))
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	cmd := newSessionListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "hello")
}

func TestSessionSearchCmd_NoMatches(t *testing.T) {
	opts := makeOpts(t)
	// Prime the DB so the FTS index exists but has no rows matching.
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	cmd := newSessionSearchCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{"kubernetes"}))
	assert.Contains(t, buf.String(), "no matches")
}

func TestSessionShowCmd_MissingSession(t *testing.T) {
	opts := makeOpts(t)
	// Ensure store initialised.
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	cmd := newSessionShowCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	err = cmd.RunE(cmd, []string{"nonexistent"})
	assert.Error(t, err)
}

func TestSessionShowCmd_PrintsTranscript(t *testing.T) {
	opts := makeOpts(t)
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	sess := agent.NewSession("read-me")
	sess.Append(agent.NewUserText("hi there"))
	sess.Append(agent.NewAssistantText("hi back"))
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	cmd := newSessionShowCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{sess.ID}))
	out := buf.String()
	assert.Contains(t, out, "hi there")
	assert.Contains(t, out, "hi back")
}

func TestSessionDeleteCmd_RequiresYes(t *testing.T) {
	opts := makeOpts(t)
	cmd := newSessionDeleteCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, []string{"whatever"})
	assert.Error(t, err)
}

func TestSessionDeleteCmd_WithYes(t *testing.T) {
	opts := makeOpts(t)
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	sess := agent.NewSession("del")
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	cmd := newSessionDeleteCmd(opts)
	require.NoError(t, cmd.Flags().Set("yes", "true"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{sess.ID}))
}

func TestNewSessionCmd_HasSubcommands(t *testing.T) {
	cmd := newSessionCmd(&Options{Config: &config.Config{}})
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["list"])
	assert.True(t, names["search"])
	assert.True(t, names["show"])
	assert.True(t, names["delete"])
}
