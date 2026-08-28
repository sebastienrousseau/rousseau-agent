package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func chatOpts(t *testing.T) *Options {
	t.Helper()
	return &Options{
		Config: &config.Config{
			Provider:  "anthropic",
			Anthropic: config.AnthropicConfig{APIKey: "sk-test", Model: "claude"},
			State:     config.StateConfig{Path: filepath.Join(t.TempDir(), "sessions.db")},
		},
		Logger: silentLogger(),
	}
}

// TestChatCmd_RunsTUIUntilContextCancelled drives the whole chat RunE:
// provider, store, tool registry, approver, compressor, agent, session
// and the Bubble Tea program. os.Stdin/os.Stdout are swapped for temp
// files so the program neither reads the developer's terminal nor
// scribbles escape codes into the test log; cancelling the context is
// what brings the program down.
func TestChatCmd_RunsTUIUntilContextCancelled(t *testing.T) {
	withStdio(t)
	opts := chatOpts(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := newChatCmd(opts)
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() { done <- cmd.RunE(cmd, nil) }()

	// A fixed 200ms sleep flaked on loaded CI runners — the chat has
	// to open the store, build every wiring step, and reach the first
	// session save before cancel arrives. 2s is comfortably longer
	// than any of the observed slow starts without lengthening the
	// happy-path meaningfully.
	time.Sleep(2 * time.Second)
	cancel()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("chat did not exit after its context was cancelled")
	}

	// The store is released now, so it is safe to reopen and confirm
	// the command really got as far as creating its session.
	requireSessionPersisted(t, opts.Config.State.Path)
}

// TestChatCmd_ResumesExistingSession takes the --session branch of
// loadOrCreateSession through the command surface.
func TestChatCmd_ResumesExistingSession(t *testing.T) {
	withStdio(t)
	opts := chatOpts(t)

	store, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	existing := agent.NewSession("resume-through-chat")
	require.NoError(t, store.Save(context.Background(), existing))
	require.NoError(t, store.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := newChatCmd(opts)
	require.NoError(t, cmd.Flags().Set("session", existing.ID))
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() { done <- cmd.RunE(cmd, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("chat did not exit after its context was cancelled")
	}
}

func TestChatCmd_ProviderFailureAborts(t *testing.T) {
	opts := chatOpts(t)
	opts.Config.Provider = "not-a-provider"
	cmd := newChatCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestChatCmd_StoreFailureAborts(t *testing.T) {
	noHome(t)
	opts := chatOpts(t)
	opts.Config.State.Path = ""
	cmd := newChatCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve home")
}

func TestChatCmd_ApproverFailureAborts(t *testing.T) {
	opts := chatOpts(t)
	opts.Config.Agent.Approver = config.ApproverConfig{Mode: "ask-a-human"}
	cmd := newChatCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver")
}

func TestChatCmd_UnknownSessionAborts(t *testing.T) {
	opts := chatOpts(t)
	cmd := newChatCmd(opts)
	require.NoError(t, cmd.Flags().Set("session", "no-such-session"))
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

// requireSessionPersisted asserts the chat command created and saved a
// session — the marker that RunE got past every wiring step and into
// the Bubble Tea program.
func requireSessionPersisted(t *testing.T, path string) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup
	hits, err := store.List(context.Background(), 1)
	require.NoError(t, err)
	assert.Len(t, hits, 1, "chat must persist the session it opens")
}
