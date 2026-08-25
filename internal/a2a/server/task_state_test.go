package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

func newState(id string) *taskState {
	return &taskState{id: id, status: a2a.TaskStatusRunning, cancel: func() {}}
}

func TestTaskState_Snapshot(t *testing.T) {
	t.Run("fresh state", func(t *testing.T) {
		st := newState("t1")
		snap := st.snapshot()
		assert.Equal(t, "t1", snap.TaskID)
		assert.Equal(t, a2a.TaskStatusRunning, snap.Status)
		assert.Zero(t, snap.NumUpdates)
		assert.Equal(t, a2a.TaskUpdate{}, snap.Last)
	})

	t.Run("tracks the most recent update", func(t *testing.T) {
		st := newState("t2")
		st.emit(a2a.TaskUpdate{TaskID: "t2", Status: a2a.TaskStatusRunning, Progress: 0.1})
		st.emit(a2a.TaskUpdate{TaskID: "t2", Status: a2a.TaskStatusCompleted, OutputText: "done"})

		snap := st.snapshot()
		assert.Equal(t, a2a.TaskStatusCompleted, snap.Status)
		assert.Equal(t, "done", snap.Last.OutputText)
		assert.Equal(t, 2, snap.NumUpdates)
	})
}

func TestTaskState_History(t *testing.T) {
	t.Run("returns a defensive copy", func(t *testing.T) {
		st := newState("t")
		st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "a"})
		h := st.history()
		require.Len(t, h, 1)
		h[0].Message = "mutated"
		assert.Equal(t, "a", st.history()[0].Message)
	})

	t.Run("empty history", func(t *testing.T) {
		assert.Empty(t, newState("t").history())
	})

	t.Run("rolls off the oldest updates past the cap", func(t *testing.T) {
		st := newState("t")
		total := maxHistoryUpdates + 50
		for i := 0; i < total; i++ {
			st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: fmt.Sprintf("m%d", i)})
		}
		h := st.history()
		require.Len(t, h, maxHistoryUpdates)
		assert.Equal(t, fmt.Sprintf("m%d", total-maxHistoryUpdates), h[0].Message)
		assert.Equal(t, fmt.Sprintf("m%d", total-1), h[len(h)-1].Message)
		// snapshot's counter reflects the trimmed buffer, not the total.
		assert.Equal(t, maxHistoryUpdates, st.snapshot().NumUpdates)
	})
}

func TestTaskState_IsTerminal(t *testing.T) {
	tests := []struct {
		status a2a.TaskStatus
		want   bool
	}{
		{a2a.TaskStatusRunning, false},
		{a2a.TaskStatusCompleted, true},
		{a2a.TaskStatusFailed, true},
		{a2a.TaskStatusCancelled, true},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			st := newState("t")
			st.status = tc.status
			assert.Equal(t, tc.want, st.isTerminal())
		})
	}
}

func TestTaskState_SubscribeReceivesUpdates(t *testing.T) {
	st := newState("t")
	ch, cancel := st.subscribe()
	defer cancel()

	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "one"})
	upd := recvUpdate(t, ch)
	assert.Equal(t, "one", upd.Message)
}

func TestTaskState_SubscribeFanOutToMultipleSubscribers(t *testing.T) {
	st := newState("t")
	chans := make([]<-chan a2a.TaskUpdate, 3)
	for i := range chans {
		ch, cancel := st.subscribe()
		defer cancel() //nolint:gocritic // all subscribers live for the test
		chans[i] = ch
	}

	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "broadcast"})
	for _, ch := range chans {
		assert.Equal(t, "broadcast", recvUpdate(t, ch).Message)
	}
}

func TestTaskState_TerminalEmitClosesSubscribers(t *testing.T) {
	st := newState("t")
	ch, cancel := st.subscribe()
	defer cancel()

	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
	assert.Equal(t, a2a.TaskStatusCompleted, recvUpdate(t, ch).Status)

	select {
	case _, alive := <-ch:
		assert.False(t, alive, "channel must be closed after a terminal update")
	case <-time.After(time.Second):
		t.Fatal("channel was not closed after terminal update")
	}

	// A post-terminal emit must not panic on the closed channel: the
	// subscriber list was detached under the lock.
	assert.NotPanics(t, func() {
		st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "stray"})
	})
}

func TestTaskState_SubscribeAfterTerminalReturnsClosedChannel(t *testing.T) {
	st := newState("t")
	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusFailed, Message: "boom"})

	ch, cancel := st.subscribe()
	require.NotNil(t, cancel)
	assert.NotPanics(t, cancel, "cancel must be a safe no-op for terminal tasks")

	select {
	case _, alive := <-ch:
		assert.False(t, alive)
	case <-time.After(time.Second):
		t.Fatal("terminal subscribe must hand back a closed channel")
	}

	st.mu.Lock()
	assert.Empty(t, st.subscribers, "terminal subscribe must not register")
	st.mu.Unlock()
}

func TestTaskState_SlowSubscriberIsDroppedNotBlocked(t *testing.T) {
	st := newState("t")
	ch, cancel := st.subscribe()
	defer cancel()

	// Overfill the buffer; emit must not block.
	const overflow = 40
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < overflow; i++ {
			st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Progress: float64(i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emit blocked on a slow subscriber")
	}

	assert.Len(t, ch, cap(ch), "buffer should be full; the rest were dropped")
	// History still has everything, so a reconnecting client can catch up.
	assert.Len(t, st.history(), overflow)
}

func TestTaskState_CancelDetachesSubscriber(t *testing.T) {
	st := newState("t")
	ch1, cancel1 := st.subscribe()
	ch2, cancel2 := st.subscribe()
	defer cancel2()

	st.mu.Lock()
	require.Len(t, st.subscribers, 2)
	st.mu.Unlock()

	cancel1()
	st.mu.Lock()
	require.Len(t, st.subscribers, 1)
	st.mu.Unlock()

	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "after-detach"})
	assert.Empty(t, ch1, "detached subscriber must not receive updates")
	assert.Equal(t, "after-detach", recvUpdate(t, ch2).Message)

	// Cancelling twice is a no-op (the channel is no longer in the list).
	assert.NotPanics(t, cancel1)
}

func TestTaskState_ConcurrentEmitAndSubscribe(t *testing.T) {
	st := newState("t")
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, cancel := st.subscribe()
			defer cancel()
			// Drain so emit never has to drop.
			deadline := time.After(time.Second)
			for {
				select {
				case _, alive := <-ch:
					if !alive {
						return
					}
				case <-deadline:
					return
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Progress: float64(i)})
			_ = st.snapshot()
			_ = st.history()
			_ = st.isTerminal()
		}(i)
	}
	wg.Wait()

	st.emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
	assert.True(t, st.isTerminal())
	st.mu.Lock()
	assert.Empty(t, st.subscribers)
	st.mu.Unlock()
}

func recvUpdate(t *testing.T, ch <-chan a2a.TaskUpdate) a2a.TaskUpdate {
	t.Helper()
	select {
	case upd, alive := <-ch:
		require.True(t, alive, "channel closed unexpectedly")
		return upd
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an update")
		return a2a.TaskUpdate{}
	}
}
