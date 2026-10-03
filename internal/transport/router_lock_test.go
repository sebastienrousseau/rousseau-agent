package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// blockingJID stalls Get for one sender until released, standing in
// for a slow store (SQLite busy-wait, a remote Postgres).
type blockingJID struct {
	*memJID
	slow    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingJID) Get(ctx context.Context, jid string) (string, bool, error) {
	if jid == b.slow {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.memJID.Get(ctx, jid)
}

// TestRouter_SlowSenderDoesNotBlockOthers pins per-sender session
// locking: one sender's slow session lookup used to hold a router-wide
// mutex and stall every other sender's turn.
func TestRouter_SlowSenderDoesNotBlockOthers(t *testing.T) {
	jids := &blockingJID{memJID: newMemJID(), slow: "a", entered: make(chan struct{}), release: make(chan struct{})}
	r := NewRouter(&stubRunner{reply: agent.NewAssistantText("ok")}, newMemStore(), jids, silentLogger(), RouterOptions{})

	go func() { _, _ = r.Handle(context.Background(), IncomingMessage{From: "a", Body: "slow"}) }() //nolint:errcheck // blocked on purpose
	<-jids.entered

	done := make(chan error, 1)
	go func() {
		_, err := r.Handle(context.Background(), IncomingMessage{From: "b", Body: "fast"})
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("sender b was blocked behind sender a's session lookup")
	}
	close(jids.release)
}

func TestKeyedMutex_SerializesSameKeyAndCleansUp(t *testing.T) {
	var k keyedMutex
	unlockA := k.Lock("x")
	got := make(chan struct{})
	go func() {
		unlock := k.Lock("x")
		close(got)
		unlock()
	}()
	select {
	case <-got:
		t.Fatal("same key must wait")
	case <-time.After(30 * time.Millisecond):
	}
	unlockA()
	<-got
	k.Lock("y")()
	k.mu.Lock()
	defer k.mu.Unlock()
	assert.Empty(t, k.m, "entries are dropped once unused")
}
