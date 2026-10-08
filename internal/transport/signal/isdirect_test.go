package signal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func handleSignalFrame(t *testing.T, frame string) transport.IncomingMessage {
	t.Helper()
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	sw := &stubWriter{}
	c.stdin = &jsonWriter{w: sw, enc: json.NewEncoder(sw)}
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), []byte(frame), handler))
	require.True(t, called)
	return seen
}

func TestHandleFrame_DirectMessageIsDirect(t *testing.T) {
	m := handleSignalFrame(t, `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"sourceNumber":"+2","dataMessage":{"message":"ping"}}}}`)
	assert.True(t, m.IsDirect)
}

func TestHandleFrame_GroupMessageIsNotDirect(t *testing.T) {
	m := handleSignalFrame(t, `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"sourceNumber":"+2","dataMessage":{"message":"ping","groupInfo":{"groupId":"grp==","type":"DELIVER"}}}}}`)
	assert.False(t, m.IsDirect)
}
