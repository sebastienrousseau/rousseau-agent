package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// TestA2A_PerPeerLimit: running tasks are capped per peer and across
// the server; over a cap a new task is refused, not queued.
func TestA2A_PerPeerLimit(t *testing.T) {
	h, release := blockingHandler(t)
	ts, s := newAuthedTestServer(t, h)
	s.MaxInflightPerPeer = 2
	s.MaxInflight = 3

	submit := func(tok string) int {
		status, _ := doAuthed(t, ts, tok, http.MethodPost, "/tasks", mustMarshal(t, a2a.Task{Prompt: "p"}))
		return status
	}
	require.Equal(t, http.StatusAccepted, submit(tokPeerA))
	require.Equal(t, http.StatusAccepted, submit(tokPeerA))
	assert.Equal(t, http.StatusTooManyRequests, submit(tokPeerA), "third task for peer A exceeds the per-peer cap")

	status, _ := doAuthed(t, ts, tokPeerA, http.MethodPost, "/message:send", specMessageBody(t, "", ""))
	assert.Equal(t, http.StatusTooManyRequests, status, "POST /message:send is capped too")
	_, body := doAuthed(t, ts, tokPeerA, http.MethodPost, "/jsonrpc", jsonrpcBody(t, a2a.MethodSendMessage, a2a.Message{
		MessageID: "m-lim", Role: a2a.RoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "x"}},
	}))
	assert.Equal(t, a2a.JSONRPCErrRateLimited, jsonrpcErrCode(t, body), "JSON-RPC SendMessage is capped too")

	require.Equal(t, http.StatusAccepted, submit(tokPeerB), "peer B has its own allowance")
	assert.Equal(t, http.StatusTooManyRequests, submit(tokPeerB), "global cap of 3 reached")

	close(release)
	require.Eventually(t, func() bool { return submit(tokPeerA) == http.StatusAccepted },
		5*time.Second, 20*time.Millisecond, "finished tasks free their slots")
}

// TestA2A_DefaultLimitsApply: the zero-value Server still caps work.
func TestA2A_DefaultLimitsApply(t *testing.T) {
	h, _ := blockingHandler(t)
	s := newServer(t, h, nil)
	var refused bool
	for i := 0; i < defaultMaxInflightPerPeer+1; i++ {
		if _, err := s.spawnTask(context.Background(), a2a.Task{TaskID: newTaskID(), Prompt: "p"}); err != nil {
			refused = true
		}
	}
	assert.True(t, refused, "default per-peer cap must refuse task %d", defaultMaxInflightPerPeer+1)
}

// TestHTTPServer_Timeouts: slowloris and idle connections are bounded;
// no WriteTimeout because SSE streams outlive any fixed deadline.
func TestHTTPServer_Timeouts(t *testing.T) {
	srv := HTTPServer(http.NotFoundHandler())
	assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, srv.ReadTimeout)
	assert.Equal(t, 120*time.Second, srv.IdleTimeout)
	assert.Zero(t, srv.WriteTimeout, "SSE needs no write deadline")
}
