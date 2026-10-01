package claudecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForkSession_CopiesTranscriptUnderNewID pins /save on claudecli:
// claude keeps the conversation in its own transcript, so a snapshot
// needs a copy of that file under the snapshot's id (verified live: a
// transcript copied this way resumes with its history).
func TestForkSession_CopiesTranscriptUnderNewID(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	src := sessionFilePathDefault("aaa")
	require.NotEmpty(t, src)
	assert.True(t, strings.HasPrefix(src, cfgDir), "CLAUDE_CONFIG_DIR must be honoured, got %s", src)
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o700))
	require.NoError(t, os.WriteFile(src, []byte(
		`{"type":"user","sessionId":"aaa","message":"remember pineapple"}`+"\n"+
			`{"type":"assistant","sessionId":"aaa","message":"ok"}`+"\n"), 0o600))

	p := New(Config{})
	require.NoError(t, p.ForkSession("aaa", "bbb"))

	got, err := os.ReadFile(sessionFilePathDefault("bbb"))
	require.NoError(t, err)
	assert.NotContains(t, string(got), `"sessionId":"aaa"`)
	assert.Equal(t, 2, strings.Count(string(got), `"sessionId":"bbb"`))
	assert.Contains(t, string(got), "pineapple")
	assert.True(t, p.knowsSession("bbb"), "the next turn on the snapshot must --resume it")
	orig, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Contains(t, string(orig), `"sessionId":"aaa"`, "the source transcript is untouched")
}

func TestForkSession_NoTranscriptIsNoop(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	p := New(Config{})
	assert.NoError(t, p.ForkSession("never-ran", "snap"))
	assert.False(t, p.knowsSession("snap"))
}

// TestEraseTranscripts pins that erasure reaches claude's own copy of
// a conversation, in any project directory (the CLI may run from a
// different cwd than the daemon) and including rotated copies, while
// other sessions' transcripts stay.
func TestEraseTranscripts(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	write := func(rel string) string {
		p := filepath.Join(cfgDir, "projects", rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
		return p
	}
	a1 := write("-workspace/aaa.jsonl")
	a2 := write("-other-cwd/aaa.jsonl")
	a3 := write("-workspace/aaa.jsonl.rotated-20260101T000000Z")
	keep := write("-workspace/bbb.jsonl")

	n, err := EraseTranscripts([]string{"aaa"})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	for _, p := range []string{a1, a2, a3} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), p)
	}
	_, err = os.Stat(keep)
	assert.NoError(t, err)

	n, err = EraseTranscripts([]string{"../escape", ""})
	require.NoError(t, err)
	assert.Zero(t, n, "ids that are not plain names are ignored")
}
