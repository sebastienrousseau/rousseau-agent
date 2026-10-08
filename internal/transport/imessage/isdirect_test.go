package imessage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func handleIMessage(t *testing.T, chatGUID string) transport.IncomingMessage {
	t.Helper()
	c := newTestClient(t, Config{})
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	c.handleMessage(context.Background(), messageRecord{
		GUID:   "m1",
		Text:   "hi",
		Handle: handleRecord{Address: "+15550001"},
		Chats:  []chatRecord{{GUID: chatGUID}},
	}, handler)
	require.True(t, called)
	return seen
}

func TestHandleMessage_OneToOneChatIsDirect(t *testing.T) {
	assert.True(t, handleIMessage(t, "iMessage;-;+15550001").IsDirect)
}

func TestHandleMessage_GroupChatIsNotDirect(t *testing.T) {
	assert.False(t, handleIMessage(t, "iMessage;+;chat123456789").IsDirect)
}
