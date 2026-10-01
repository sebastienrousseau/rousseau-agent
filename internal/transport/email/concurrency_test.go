package email

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
// Two mails from different senders arrive in one poll.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	fake := &scriptedIMAP{}
	fake.seqNums = []uint32{1, 2}
	fake.messages = []*imapclient.FetchMessageBuffer{
		mkMessage("slow@example.test", "s", "slow"),
		mkMessage("fast@example.test", "f", "fast"),
	}
	c := clientWith(t, Config{
		PollInterval:      10 * time.Millisecond,
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) { return fake, nil },
	}, silentLogger())

	fastDone := make(chan struct{})
	var once sync.Once
	var sawFastWhileSlowRan atomic.Bool
	// The fake re-serves both mails on every poll; only the first slow
	// turn is meaningful (a later one would see fast from a previous
	// poll and pass even with inline handling).
	var slowCalls atomic.Int32
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		if m.From == "fast@example.test" {
			once.Do(func() { close(fastDone) })
			return "", nil
		}
		if slowCalls.Add(1) > 1 {
			return "", nil
		}
		select {
		case <-fastDone:
			sawFastWhileSlowRan.Store(true)
		case <-time.After(2 * time.Second):
		}
		return "", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, handler) }()
	assert.Eventually(t, sawFastWhileSlowRan.Load, 3*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}
