package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// Test-only bearer tokens; not secrets.
const (
	tokPeerA = "test-token-peer-a"
	tokPeerB = "test-token-peer-b"
)

// peerCapture records the Peer of every task the server dispatches.
type peerCapture struct{ peers chan string }

func (p peerCapture) OnTask(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	p.peers <- task.Peer
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
	return nil
}

// mustSpawn spawns a task as the unauthenticated peer. Uses Errorf,
// not FailNow, so it is safe from goroutines.
func mustSpawn(t *testing.T, s *Server, task a2a.Task) *taskState {
	t.Helper()
	state, err := s.spawnTask(context.Background(), task)
	if err != nil {
		t.Errorf("spawnTask(%s): %v", task.TaskID, err)
	}
	return state
}

func newAuthedTestServer(t *testing.T, h Handler) (*httptest.Server, *Server) {
	t.Helper()
	s, err := New(a2a.CapabilityCard{AgentID: "owner-test", Name: "owner-test"}, h, []string{tokPeerA, tokPeerB})
	require.NoError(t, err)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, s
}

// doAuthed sends one request with the given bearer token and returns
// the status and body.
func doAuthed(t *testing.T, ts *httptest.Server, tok, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if req.Method == http.MethodGet && resp.Header.Get("Content-Type") == "text/event-stream" {
		return resp.StatusCode, nil
	}
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, out
}

func specMessageBody(t *testing.T, taskID, contextID string) []byte {
	t.Helper()
	return mustMarshal(t, a2a.Message{
		MessageID: "m-1",
		TaskID:    taskID,
		ContextID: contextID,
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
	})
}

func jsonrpcBody(t *testing.T, method string, params any) []byte {
	t.Helper()
	return mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
}

func jsonrpcErrCode(t *testing.T, body []byte) int {
	t.Helper()
	var resp a2a.JSONRPCResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	if resp.Error == nil {
		return 0
	}
	return resp.Error.Code
}

func recvPeer(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
		return ""
	}
}

// TestA2A_PeerRecordedOnEveryRoute: every task-creating route must
// stamp the authenticated peer, not only the legacy POST /tasks.
func TestA2A_PeerRecordedOnEveryRoute(t *testing.T) {
	want := PeerID(tokPeerA)
	routes := []struct {
		name string
		path string
		body func(t *testing.T) []byte
	}{
		{"legacy POST /tasks", "/tasks", func(t *testing.T) []byte {
			return mustMarshal(t, a2a.Task{FromAgent: "spoofed", Prompt: "hi"})
		}},
		{"v1 POST /message:send", "/message:send", func(t *testing.T) []byte {
			return specMessageBody(t, "", PeerID(tokPeerB))
		}},
		{"JSON-RPC SendMessage", "/jsonrpc", func(t *testing.T) []byte {
			return jsonrpcBody(t, a2a.MethodSendMessage, a2a.Message{
				MessageID: "m-2", ContextID: PeerID(tokPeerB), Role: a2a.RoleUser,
				Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
			})
		}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			cap := peerCapture{peers: make(chan string, 1)}
			ts, _ := newAuthedTestServer(t, cap)
			status, body := doAuthed(t, ts, tokPeerA, http.MethodPost, rt.path, rt.body(t))
			require.Less(t, status, 300, "body: %s", body)
			assert.Equal(t, want, recvPeer(t, cap.peers), "task.Peer must be the authenticated peer")
		})
	}
}

// TestA2A_OtherPeerCannotReadTask: a task is visible only to the peer
// that created it, on every read/cancel/subscribe route.
func TestA2A_OtherPeerCannotReadTask(t *testing.T) {
	h, _ := blockingHandler(t)
	ts, _ := newAuthedTestServer(t, h)
	status, body := doAuthed(t, ts, tokPeerA, http.MethodPost, "/message:send", specMessageBody(t, "t-own", ""))
	require.Equal(t, http.StatusAccepted, status, "body: %s", body)

	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/tasks/t-own"},
		{http.MethodGet, "/tasks/t-own/events"},
		{http.MethodGet, "/tasks/t-own:subscribe"},
		{http.MethodPost, "/tasks/t-own/cancel"},
		{http.MethodPost, "/tasks/t-own:cancel"},
	} {
		status, _ := doAuthed(t, ts, tokPeerB, rt.method, rt.path, nil)
		assert.Equal(t, http.StatusNotFound, status, "%s %s by another peer", rt.method, rt.path)
	}
	for _, method := range []string{a2a.MethodGetTask, a2a.MethodCancelTask} {
		_, body := doAuthed(t, ts, tokPeerB, http.MethodPost, "/jsonrpc", jsonrpcBody(t, method, map[string]string{"id": "t-own"}))
		assert.Equal(t, a2a.JSONRPCErrTaskNotFound, jsonrpcErrCode(t, body), "%s by another peer", method)
	}

	status, _ = doAuthed(t, ts, tokPeerA, http.MethodGet, "/tasks/t-own", nil)
	assert.Equal(t, http.StatusOK, status, "the owner still reads its task")
}

// TestA2A_DuplicateTaskIDConflict: a client-chosen TaskID that already
// exists must not replace the running task.
func TestA2A_DuplicateTaskIDConflict(t *testing.T) {
	h, _ := blockingHandler(t)
	ts, s := newAuthedTestServer(t, h)
	status, body := doAuthed(t, ts, tokPeerA, http.MethodPost, "/tasks", mustMarshal(t, a2a.Task{TaskID: "t-dup", Prompt: "p"}))
	require.Equal(t, http.StatusAccepted, status, "body: %s", body)
	original := s.lookup("t-dup")
	require.NotNil(t, original)

	status, _ = doAuthed(t, ts, tokPeerB, http.MethodPost, "/tasks", mustMarshal(t, a2a.Task{TaskID: "t-dup", Prompt: "p"}))
	assert.Equal(t, http.StatusConflict, status, "legacy POST /tasks")
	status, _ = doAuthed(t, ts, tokPeerB, http.MethodPost, "/message:send", specMessageBody(t, "t-dup", ""))
	assert.Equal(t, http.StatusConflict, status, "POST /message:send")
	_, body = doAuthed(t, ts, tokPeerB, http.MethodPost, "/jsonrpc", jsonrpcBody(t, a2a.MethodSendMessage, a2a.Message{
		MessageID: "m-3", TaskID: "t-dup", Role: a2a.RoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "x"}},
	}))
	assert.Equal(t, a2a.JSONRPCErrInvalidParams, jsonrpcErrCode(t, body), "JSON-RPC SendMessage")

	assert.Same(t, original, s.lookup("t-dup"), "the original task must survive")
}
