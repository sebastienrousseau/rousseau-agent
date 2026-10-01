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
