package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewChatCmd_HasFlags(t *testing.T) {
	cmd := newChatCmd(&Options{})
	assert.NotNil(t, cmd.Flags().Lookup("session"))
	assert.NotNil(t, cmd.Flags().Lookup("title"))
}

func TestSystemPrompt_Override(t *testing.T) {
	assert.Equal(t, "custom", systemPrompt("custom"))
}

func TestSystemPrompt_Default(t *testing.T) {
	assert.Contains(t, systemPrompt(""), "rousseau")
}

func TestOpenStore_ExpandsHomeDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := openStore(context.Background(), "")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
}

func TestOpenStore_Explicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "sessions.db")
	store, err := openStore(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

func TestLoadOrCreateSession_New(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(context.Background(), filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	sess, err := loadOrCreateSession(context.Background(), store, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
	assert.NotEmpty(t, sess.Title)
}

func TestLoadOrCreateSession_Resume(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(context.Background(), filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	original := agent.NewSession("resume-me")
	require.NoError(t, store.Save(context.Background(), original))

	got, err := loadOrCreateSession(context.Background(), store, original.ID, "")
	require.NoError(t, err)
	assert.Equal(t, original.ID, got.ID)
	assert.Equal(t, "resume-me", got.Title)
}

func TestLoadOrCreateSession_ResumeMissing(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(context.Background(), filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	_, err = loadOrCreateSession(context.Background(), store, "nonexistent", "")
	assert.Error(t, err)
}

func TestChatCmd_MissingAPIKeyErrors(t *testing.T) {
	opts := &Options{
		Config: &config.Config{}, // empty — no api key
		Logger: silentLogger(),
	}
	cmd := newChatCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
}

func TestChatCmd_BadAnthropicKey_StillErrors(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		Config: &config.Config{
			Anthropic: config.AnthropicConfig{APIKey: ""}, // still empty triggers early return
			State:     config.StateConfig{Path: filepath.Join(dir, "s.db")},
		},
		Logger: silentLogger(),
	}
	cmd := newChatCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
}
