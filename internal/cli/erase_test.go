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
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// TestSessionDeleteBySender pins the documented GDPR erasure command
// end to end: the store rows and claude's transcript both go, another
// sender's data stays, and --yes is required.
func TestSessionDeleteBySender(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+dbPath+"\n"), 0o600))
	claudeDir := filepath.Join(dir, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("ANTHROPIC_API_KEY", "")

	st, err := sqlitestore.Open(ctx, dbPath)
	require.NoError(t, err)
	require.NoError(t, st.EnsureSearch(ctx))
	mk := func(sender string) string {
		s := agent.NewSession("chat")
		s.Sender = sender
		s.Append(agent.NewUserText("hi"))
		require.NoError(t, st.Save(ctx, s))
		p := filepath.Join(claudeDir, "projects", "-workspace", s.ID+".jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
		return s.ID
	}
	alice := mk("alice@s.whatsapp.net")
	bob := mk("bob@s.whatsapp.net")
	require.NoError(t, st.Close())

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		root := NewRoot(&Options{})
		root.SetArgs(append([]string{"--config", cfgPath, "session", "delete-by-sender"}, args...))
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		err := root.ExecuteContext(ctx)
		return out.String(), err
	}
	_, err = run("alice@s.whatsapp.net")
	assert.ErrorContains(t, err, "--yes")

	out, err := run("alice@s.whatsapp.net", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "1 session(s), 1 claude transcript file(s)")

	_, err = os.Stat(filepath.Join(claudeDir, "projects", "-workspace", alice+".jsonl"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(claudeDir, "projects", "-workspace", bob+".jsonl"))
	assert.NoError(t, err, "other senders' transcripts stay")

	st, err = sqlitestore.Open(ctx, dbPath)
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck // test cleanup
	_, err = st.Load(ctx, alice)
	assert.Error(t, err)
	_, err = st.Load(ctx, bob)
	assert.NoError(t, err)
}
