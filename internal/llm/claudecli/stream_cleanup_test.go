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
// Stream hands image files to the CLI *by path* (named in the stdin
// prompt), and
// returns as soon as the child is started. cleanup() used to be
// deferred inside Stream, so the temp directory was removed at that
// return -- before the child had necessarily opened the files. Whether
// it broke depended on scheduling, which is the worst kind of bug.
//
// The stand-in CLI below asserts the condition directly: it stats every
// image path named on stdin and refuses to emit a result line if any is
// missing (or if none was named, so the test cannot pass vacuously). A regression makes this test fail rather than flake.
func TestStream_ImageFilesOutliveTheChild(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")

	// Read the prompt; for each attached image path, fail loudly if it
	// is gone. Sleep first so a premature cleanup has time to land.
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
paths=$(sed -n 's/^\[Attached image: \(.*\)\. Open it.*/\1/p')
sleep 0.3
[ -n "$paths" ] || { echo "NO IMAGE PATH IN PROMPT" >&2; exit 4; }
for a in $paths; do
  if [ ! -f "$a" ]; then
    echo "IMAGE MISSING: $a" >&2
    exit 3
  fi
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
			"the CLI reported a missing image file: cleanup ran before the child finished")
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
sed -n 's/^\[Attached image: \(.*\)\. Open it.*/\1/p' | tr -d '\n' > "`+seen+`"
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
	require.NoError(t, err, "the stand-in CLI should have recorded the image path")
	_, statErr := os.Stat(string(raw))
	assert.True(t, os.IsNotExist(statErr),
		"image temp file %s should be removed once the stream completes", string(raw))
}

// TestStream_CancelSendsSIGTERMFirst pins graceful cancellation: on a
// turn timeout or /cancel the claude child gets SIGTERM (so it can
// stop its own tool subprocesses and flush its transcript) before any
// SIGKILL. exec.CommandContext's default is an immediate SIGKILL.
func TestStream_CancelSendsSIGTERMFirst(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	marker := filepath.Join(dir, "got-term")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
cat >/dev/null
trap 'echo term > "`+marker+`"; exit 143' TERM
sleep 30 &
wait
`), 0o755))

	old := killGrace
	killGrace = 2 * time.Second
	t.Cleanup(func() { killGrace = old })

	p := &Provider{cfg: Config{Binary: script}}
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	events, report, err := p.Stream(ctx, agent.Request{
		Messages: []agent.Message{agent.NewUserText("long job")},
	})
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond) // let the trap install
	cancel()
	for range events {
	}
	select {
	case <-report:
	case <-time.After(15 * time.Second):
		t.Fatal("stream did not finish after cancel")
	}
	_, statErr := os.Stat(marker)
	assert.NoError(t, statErr, "child should have received SIGTERM before being killed")
	// The grandchild (sleep 30) must not hold the turn open: the whole
	// group is signalled, so the stream ends well inside the grace.
	assert.Less(t, time.Since(start), 10*time.Second)
}
