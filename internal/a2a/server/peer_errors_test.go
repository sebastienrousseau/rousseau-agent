package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// secretDetail stands in for internal error text (addresses, paths,
// upstream messages) that must reach the logs but never a peer.
const secretDetail = "dial tcp 10.9.8.7:5432: internal-secret-detail"

// TestPeerErrors_HandlerErrorNotLeaked: a Handler error reaches the
// peer as a generic message with a reference; the detail is logged
// under the same reference.
func TestPeerErrors_HandlerErrorNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	s := newServer(t, handlerFunc(func(context.Context, a2a.Task, func(a2a.TaskUpdate)) error {
		return errors.New(secretDetail)
	}), nil)
	s.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	state := mustSpawn(t, s, a2a.Task{TaskID: "t-leak", Prompt: "p"})
	waitFor(t, state.isTerminal)

	msg := state.snapshot().Last.Message
	assert.NotContains(t, msg, "internal-secret-detail")
	assert.Contains(t, msg, "ref ")
	ref := msg[strings.LastIndex(msg, "ref ")+4 : len(msg)-1]
	assert.Contains(t, logs.String(), "internal-secret-detail")
	assert.Contains(t, logs.String(), ref, "log line carries the peer-visible reference")
}

// TestPeerErrors_SubmitDecodeGeneric: a body decode error does not
// echo Go decoder internals back to the peer.
func TestPeerErrors_SubmitDecodeGeneric(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"prompt":7}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid task body")
	assert.NotContains(t, rec.Body.String(), "Go struct field")
}

// TestPeerErrors_SpecMessageDecodeGeneric: same on POST /message:send.
func TestPeerErrors_SpecMessageDecodeGeneric(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/message:send", strings.NewReader(`{"parts":7}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid message body")
	assert.NotContains(t, rec.Body.String(), "Go struct field")
}

// TestPeerErrors_JSONRPCParseGeneric: a JSON-RPC parse error carries a
// generic message, not the decoder's.
func TestPeerErrors_JSONRPCParseGeneric(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jsonrpc", strings.NewReader(`{bad`)))
	var resp a2a.JSONRPCResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrParseError, resp.Error.Code)
	assert.NotContains(t, resp.Error.Message, "invalid character")
}
