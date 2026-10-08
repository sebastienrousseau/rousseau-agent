package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// A finished task must leave the map after TaskRetention; before this
// the map only ever grew.
func TestSpawnTask_EvictsTerminalTasksAfterRetention(t *testing.T) {
	s := newServer(t, handlerFunc(func(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
		return nil
	}), nil)
	s.TaskRetention = 30 * time.Millisecond

	state := mustSpawn(t, s, a2a.Task{TaskID: "t-evict", Prompt: "p"})
	waitFor(t, state.isTerminal)
	assert.NotNil(t, s.lookup("t-evict"), "still queryable inside the retention window")

	require.Eventually(t, func() bool { return s.lookup("t-evict") == nil },
		2*time.Second, 5*time.Millisecond)
}

// Task handlers derive from the Serve context and are joined on
// shutdown, so a cancelled server does not leave handler goroutines
// running on context.Background.
func TestServeListener_CancelsAndJoinsRunningTasks(t *testing.T) {
	var (
		mu       sync.Mutex
		sawCtx   bool
		returned bool
	)
	s := newServer(t, handlerFunc(func(ctx context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		<-ctx.Done()
		mu.Lock()
		sawCtx = true
		mu.Unlock()
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCancelled})
		mu.Lock()
		returned = true
		mu.Unlock()
		return nil
	}), nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeListener(ctx, ln) }()

	// Wait until Serve has installed its base context, then spawn.
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.baseCtx != nil
	}, time.Second, 5*time.Millisecond)
	state := mustSpawn(t, s, a2a.Task{TaskID: "t-join", Prompt: "p"})

	cancel()
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, sawCtx, "handler context was cancelled by shutdown")
	assert.True(t, returned, "Serve returned only after the handler finished")
	assert.True(t, state.isTerminal())
}
