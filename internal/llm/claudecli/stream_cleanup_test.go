package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestStream_ImageFilesOutliveTheChild is a regression test for a
// use-after-free.
//
// Stream hands image files to the CLI *by path* via --image, and
// returns as soon as the child is started. cleanup() used to be
// deferred inside Stream, so the temp directory was removed at that
// return -- before the child had necessarily opened the files. Whether
// it broke depended on scheduling, which is the worst kind of bug.
//
// The stand-in CLI below asserts the condition directly: it stats every
// --image path it was given and refuses to emit a result line if any is
// missing. A regression makes this test fail rather than flake.
func TestStream_ImageFilesOutliveTheChild(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")

	// Walk argv; for each --image VALUE, fail loudly if VALUE is gone.
	// Sleep first so a premature cleanup has time to land.
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
sleep 0.3
prev=""
for a in "$@"; do
  if [ "$prev" = "--image" ]; then
    if [ ! -f "$a" ]; then
      echo "IMAGE MISSING: $a" >&2
      exit 3
    fi
  fi
  prev="$a"
done
printf '{"type":"result","subtype":"success","result":"ok","session_id":"s1"}\n'
`), 0o755))

	p := &Provider{cfg: Config{Binary: script}}

	events, report, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Kind: agent.ContentText, Text: "describe this"},
				{Kind: agent.ContentImage, Image: &agent.Image{
					MediaType: "image/png",
					Data:      []byte("\x89PNG\r\n\x1a\nfake"),
				}},
			},
		}},
	})
	require.NoError(t, err)

	for range events { // drain
	}

	select {
	case rep := <-report:
		require.NoError(t, rep.Err,
			"the CLI reported a missing --image file: cleanup ran before the child finished")
		require.NotEmpty(t, rep.Response.Message.Content)
		assert.Equal(t, "ok", rep.Response.Message.Content[0].Text)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the stream report")
	}
}

// TestStream_CleanupRunsAfterCompletion confirms the fix does not leak:
// once the report has been delivered, the temp dir is gone.
func TestStream_CleanupRunsAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	seen := filepath.Join(dir, "seen-path")

	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
prev=""
for a in "$@"; do
  if [ "$prev" = "--image" ]; then printf '%s' "$a" > "`+seen+`"; fi
  prev="$a"
done
printf '{"type":"result","subtype":"success","result":"ok","session_id":"s1"}\n'
`), 0o755))

	p := &Provider{cfg: Config{Binary: script}}
	events, report, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Kind: agent.ContentText, Text: "hi"},
				{Kind: agent.ContentImage, Image: &agent.Image{
					MediaType: "image/png",
					Data:      []byte("x"),
				}},
			},
		}},
	})
	require.NoError(t, err)
	for range events {
	}
	rep := <-report
	require.NoError(t, rep.Err)

	raw, err := os.ReadFile(seen)
	require.NoError(t, err, "the stand-in CLI should have recorded the --image path")
	_, statErr := os.Stat(string(raw))
	assert.True(t, os.IsNotExist(statErr),
		"image temp file %s should be removed once the stream completes", string(raw))
}
