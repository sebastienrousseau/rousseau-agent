package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

func lastReply(t *testing.T, w *nopWriteCloser) mcp.Envelope {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(w.buf.String()), "\n")
	var env mcp.Envelope
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &env))
	return env
}

// A server may ping its client; the client must answer with an empty
// result instead of treating the request as an orphan response.
func TestReadLoop_AnswersServerPing(t *testing.T) {
	w := &nopWriteCloser{}
	c := newTestClient(w)
	c.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":"srv-1","method":"ping"}` + "\n"))

	reply := lastReply(t, w)
	assert.Equal(t, `"srv-1"`, string(reply.ID), "string ids from the server are echoed verbatim")
	assert.JSONEq(t, `{}`, string(reply.Result))
	assert.Nil(t, reply.Error)
}

// Requests the client does not implement get "method not found", so a
// server waiting on them is not left hanging.
func TestReadLoop_RefusesUnknownServerRequest(t *testing.T) {
	w := &nopWriteCloser{}
	c := newTestClient(w)
	c.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"roots/list"}` + "\n"))

	reply := lastReply(t, w)
	assert.Equal(t, "9", string(reply.ID))
	require.NotNil(t, reply.Error)
	assert.Equal(t, mcp.CodeMethodNotFound, reply.Error.Code)
	assert.Contains(t, reply.Error.Message, "roots/list")
}

// A failed reply write is logged, not fatal to the read loop.
func TestReadLoop_ServerRequestReplyWriteFailureIsLogged(t *testing.T) {
	c := newTestClient(failWriteCloser{err: assert.AnError})
	c.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
}
