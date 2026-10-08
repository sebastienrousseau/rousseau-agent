package cron

import (
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

var failureRefRE = regexp.MustCompile(`^failed \(ref ([0-9a-f]{6})\)$`)

// The underlying error mimics the claude CLI provider, which embeds
// the child's output. Everything in it is fake.
func TestScheduler_FailureReachesChatOnlyAsRef(t *testing.T) {
	const detail = "child stderr: FAKE-DETAIL-sk-ant-api03-FAKEFAKEFAKE"
	runner := &stubRunner{err: errors.New("claude cli exited 1: " + detail)}
	rec := &recorderPub{}
	logger, logs := capturingLogger()
	s := New(Config{Runner: runner, Logger: logger, Progress: rec})

	s.fire(sqlitestore.CronJob{ID: "j", Name: "nightly", DeliverTo: "wa:1"})

	var failed *progress.Event
	for i := range rec.events {
		if rec.events[i].Kind == progress.KindError {
			failed = &rec.events[i]
		}
	}
	require.NotNil(t, failed, "a failure event must still reach the chat")
	for _, field := range []string{failed.Err, failed.Text, failed.Detail} {
		assert.NotContains(t, field, "FAKE-DETAIL")
		assert.NotContains(t, field, "claude cli exited")
	}
	m := failureRefRE.FindStringSubmatch(failed.Err)
	require.Len(t, m, 2, "chat text must be the generic form, got %q", failed.Err)
	ref := m[1]

	out := logs.String()
	assert.Contains(t, out, "cron.run_failed")
	assert.Contains(t, out, "ref="+ref, "the log line must carry the same ref")
	assert.Contains(t, out, "FAKE-DETAIL", "the full error is still logged")
}
