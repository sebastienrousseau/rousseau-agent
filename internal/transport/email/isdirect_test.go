package email

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// An email is addressed to the bot's mailbox, so it is always a
// one-to-one conversation.
func TestPollOnce_EmailIsDirect(t *testing.T) {
	fake := &fakeIMAP{
		seqNums:  []uint32{1},
		messages: []*imapclient.FetchMessageBuffer{mkMessage("alice@example.com", "hi", "body")},
	}
	c := mkClient(t, fake, nil, nil)
	var seen transport.IncomingMessage
	called := false
	require.NoError(t, c.pollOnce(context.Background(), transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})))
	require.True(t, called)
	assert.True(t, seen.IsDirect)
}
