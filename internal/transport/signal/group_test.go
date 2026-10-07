package signal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// A message posted in a group is answered in the group, not by direct
// message to its sender, and the handler sees the group as the
// conversation.
func TestHandleFrame_GroupMessageRepliesToGroup(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	sw := &stubWriter{}
	c.stdin = &jsonWriter{w: sw, enc: json.NewEncoder(sw)}

	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "to the group", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","sourceNumber":"+2","timestamp":1700000000000,"dataMessage":{"message":"ping","groupInfo":{"groupId":"grp==","type":"DELIVER"}}}}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))

	assert.Equal(t, "+2", seen.From)
	assert.Equal(t, "grp==", seen.Conversation)
	assert.Equal(t, "1700000000000", seen.MessageID)
	got := sw.buf.String()
	assert.Contains(t, got, `"groupId":"grp=="`)
	assert.NotContains(t, got, `"recipient"`)
}

func TestHandleFrame_DirectMessageConversationIsSender(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	sw := &stubWriter{}
	c.stdin = &jsonWriter{w: sw, enc: json.NewEncoder(sw)}

	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "dm", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","sourceNumber":"+2","dataMessage":{"message":"ping"}}}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
	assert.Equal(t, "+2", seen.Conversation)
	assert.Contains(t, sw.buf.String(), `"recipient":["+2"]`)
}

func TestDeliverGroup_NotConnected(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	assert.Error(t, c.DeliverGroup(context.Background(), "g", "hi"))
}
