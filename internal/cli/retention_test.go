package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

type fakeIdleEraser struct {
	mu      sync.Mutex
	cutoffs []time.Time
}

func (f *fakeIdleEraser) EraseIdleSessions(_ context.Context, cutoff time.Time) (sqlitestore.EraseReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutoffs = append(f.cutoffs, cutoff)
	return sqlitestore.EraseReport{SessionIDs: []string{"gone"}}, nil
}

func (f *fakeIdleEraser) calls() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.cutoffs...)
}

// TestRunSessionRetention pins state.session_ttl: an immediate prune
// with cutoff = now - ttl, more on each tick, and a clean stop on
// cancel.
func TestRunSessionRetention(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	f := &fakeIdleEraser{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSessionRetention(ctx, f, 30*24*time.Hour, 10*time.Millisecond, silentLogger())
		close(done)
	}()
	assert.Eventually(t, func() bool { return len(f.calls()) >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	first := f.calls()[0]
	assert.WithinDuration(t, time.Now().Add(-30*24*time.Hour), first, time.Minute)
}
