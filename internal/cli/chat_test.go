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
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup
}

func TestOpenStore_Explicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "sessions.db")
	store, err := openStore(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

func TestLoadOrCreateSession_New(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(context.Background(), filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	sess, err := loadOrCreateSession(context.Background(), store, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
	assert.NotEmpty(t, sess.Title)
}

func TestLoadOrCreateSession_Resume(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(context.Background(), filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

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
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	_, err = loadOrCreateSession(context.Background(), store, "nonexistent", "")
	assert.Error(t, err)
}

func TestBuildProvider_DefaultsToClaudeCLI(t *testing.T) {
	p, err := buildProvider(&config.Config{})
	require.NoError(t, err)
	assert.Equal(t, "claudecli", p.Name())
}

func TestBuildProvider_ClaudeCLIExplicit(t *testing.T) {
	p, err := buildProvider(&config.Config{Provider: "claudecli"})
	require.NoError(t, err)
	assert.Equal(t, "claudecli", p.Name())
}

func TestBuildProvider_AnthropicRequiresKey(t *testing.T) {
	_, err := buildProvider(&config.Config{Provider: "anthropic"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
}

func TestBuildProvider_AnthropicWithKey(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider:  "anthropic",
		Anthropic: config.AnthropicConfig{APIKey: "sk-test"},
	})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
}

func TestBuildProvider_Unknown(t *testing.T) {
	_, err := buildProvider(&config.Config{Provider: "gemini"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}
