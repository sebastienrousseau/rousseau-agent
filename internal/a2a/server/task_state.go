package server

import (
	"sync"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// taskState tracks one in-flight task on the server. Emits fan out to
// every subscriber and are also appended to `updates` so late-joining
// SSE clients can replay history.
//
// Bounded history keeps memory in check for long-running tasks; the
// oldest updates roll off once the buffer is full.
type taskState struct {
	id     string
	task   a2a.Task
	cancel func()

	mu          sync.Mutex
	status      a2a.TaskStatus
	last        a2a.TaskUpdate
	updates     []a2a.TaskUpdate
	subscribers []chan a2a.TaskUpdate
}

const maxHistoryUpdates = 256

// snapshot returns the most recent known state — safe to serialise.
type snapshot struct {
	TaskID     string         `json:"task_id"`
	Status     a2a.TaskStatus `json:"status"`
	Last       a2a.TaskUpdate `json:"last,omitempty"`
	NumUpdates int            `json:"num_updates"`
}

func (t *taskState) snapshot() snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return snapshot{
		TaskID:     t.id,
		Status:     t.status,
		Last:       t.last,
		NumUpdates: len(t.updates),
	}
}

func (t *taskState) history() []a2a.TaskUpdate {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]a2a.TaskUpdate, len(t.updates))
	copy(out, t.updates)
	return out
}

func (t *taskState) isTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return isTerminal(t.status)
}

// emit records an update and fans it out to every current subscriber.
// Subscriber channels are buffered; when a subscriber is slow enough
// to fill its buffer, its channel is dropped rather than blocking
// the handler (SSE clients that lag are expected to reconnect and
// replay via history()).
func (t *taskState) emit(upd a2a.TaskUpdate) {
	t.mu.Lock()
	t.status = upd.Status
	t.last = upd
	t.updates = append(t.updates, upd)
	if len(t.updates) > maxHistoryUpdates {
		t.updates = t.updates[len(t.updates)-maxHistoryUpdates:]
	}
	subs := append([]chan a2a.TaskUpdate(nil), t.subscribers...)
	terminal := isTerminal(upd.Status)
	if terminal {
		// Detach subscribers under lock so no new update reaches them
		// after close.
		t.subscribers = nil
	}
	t.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- upd:
		default:
			// Slow subscriber — drop the message. They'll pick it up
			// from history() on reconnect.
		}
	}
	if terminal {
		for _, ch := range subs {
			close(ch)
		}
	}
}

// subscribe registers a new update channel and returns a cancel that
// detaches it (used by SSE handlers when the peer disconnects).
func (t *taskState) subscribe() (<-chan a2a.TaskUpdate, func()) {
	ch := make(chan a2a.TaskUpdate, 16)
	t.mu.Lock()
	// If we're already terminal, hand back a channel that will just
	// close once the caller drains history. Nothing more will arrive.
	if isTerminal(t.status) {
		t.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	t.subscribers = append(t.subscribers, ch)
	t.mu.Unlock()

	cancel := func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, existing := range t.subscribers {
			if existing == ch {
				t.subscribers = append(t.subscribers[:i], t.subscribers[i+1:]...)
				break
			}
		}
	}
	return ch, cancel
}
