package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// jsonrpcCall wraps a raw POST to /jsonrpc so each test asserts on
// the same handshake without re-marshaling the envelope.
func jsonrpcCall(t *testing.T, ts *httptest.Server, method string, params any, id any) a2a.JSONRPCResponse {
	t.Helper()
	paramsRaw, err := json.Marshal(params)
	require.NoError(t, err)
	idRaw, err := json.Marshal(id)
	require.NoError(t, err)
	req := a2a.JSONRPCRequest{
		JSONRPC: a2a.JSONRPCVersion,
		Method:  method,
		Params:  paramsRaw,
		ID:      idRaw,
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	res := mustPost(t, ts.URL+"/jsonrpc", a2a.ContentTypeSpec, bytes.NewReader(body))
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode, "JSON-RPC always returns 200 — protocol errors live on the envelope")
	assert.Equal(t, a2a.ContentTypeSpec, res.Header.Get("Content-Type"))
	assert.Equal(t, a2a.SpecVersion, res.Header.Get("A2A-Version"))

	var resp a2a.JSONRPCResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resp))
	assert.Equal(t, a2a.JSONRPCVersion, resp.JSONRPC)
	assert.Equal(t, string(idRaw), string(resp.ID), "response id must equal request id")
	return resp
}

func TestJSONRPC_SendMessage_HappyPath(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	resp := jsonrpcCall(t, ts, a2a.MethodSendMessage, a2a.Message{
		MessageID: "m-1", ContextID: "ctx-1", Role: a2a.RoleUser,
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello via jsonrpc"}},
	}, "call-1")
	require.Nil(t, resp.Error, "unexpected error: %+v", resp.Error)

	var task a2a.SpecTask
	require.NoError(t, json.Unmarshal(resp.Result, &task))
	assert.NotEmpty(t, task.ID)
	assert.Equal(t, "ctx-1", task.ContextID)
	assert.Equal(t, a2a.TaskStateSubmitted, task.Status.State)
}

func TestJSONRPC_SendMessage_EmptyPromptRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	resp := jsonrpcCall(t, ts, a2a.MethodSendMessage, a2a.Message{
		MessageID: "m-x", Role: a2a.RoleUser,
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: ""}},
	}, 1)
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrInvalidParams, resp.Error.Code)
}

func TestJSONRPC_GetTask_HappyPath(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	// Submit first so we have a real task id.
	sub := jsonrpcCall(t, ts, a2a.MethodSendMessage, a2a.TextMessage("cover me"), 1)
	require.Nil(t, sub.Error)
	var submitted a2a.SpecTask
	require.NoError(t, json.Unmarshal(sub.Result, &submitted))

	// Give the handler a moment so state.Status leaves SUBMITTED.
	time.Sleep(30 * time.Millisecond)

	got := jsonrpcCall(t, ts, a2a.MethodGetTask, map[string]string{"id": submitted.ID}, 2)
	require.Nil(t, got.Error, "unexpected error: %+v", got.Error)
	var fetched a2a.SpecTask
	require.NoError(t, json.Unmarshal(got.Result, &fetched))
	assert.Equal(t, submitted.ID, fetched.ID)
	assert.NotEqual(t, a2a.TaskStateUnspecified, fetched.Status.State)
}

func TestJSONRPC_GetTask_UnknownID_TaskNotFoundError(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	resp := jsonrpcCall(t, ts, a2a.MethodGetTask, map[string]string{"id": "nope"}, 1)
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrTaskNotFound, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "no such task")
}

func TestJSONRPC_GetTask_MissingIDRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	resp := jsonrpcCall(t, ts, a2a.MethodGetTask, map[string]any{}, 1)
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrInvalidParams, resp.Error.Code)
}

func TestJSONRPC_CancelTask_HappyPath(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	sub := jsonrpcCall(t, ts, a2a.MethodSendMessage, a2a.TextMessage("cancel me"), 1)
	var submitted a2a.SpecTask
	require.NoError(t, json.Unmarshal(sub.Result, &submitted))

	cancel := jsonrpcCall(t, ts, a2a.MethodCancelTask, map[string]string{"id": submitted.ID}, 2)
	require.Nil(t, cancel.Error)
	var task a2a.SpecTask
	require.NoError(t, json.Unmarshal(cancel.Result, &task))
	assert.Equal(t, a2a.TaskStateCanceled, task.Status.State)
}

func TestJSONRPC_CancelTask_UnknownIDErrors(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	resp := jsonrpcCall(t, ts, a2a.MethodCancelTask, map[string]string{"id": "nope"}, 1)
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrTaskNotFound, resp.Error.Code)
}

func TestJSONRPC_UnknownMethod(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	resp := jsonrpcCall(t, ts, "MakeCoffee", struct{}{}, 1)
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrMethodNotFound, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "MakeCoffee")
}

func TestJSONRPC_StreamingMethodsNotYetSupported(t *testing.T) {
	t.Parallel()
	// Freezes the fallback-to-REST contract for streaming methods —
	// when we implement them the test flips from MethodNotFound
	// to a real streaming response, which is a deliberate lift.
	ts, _ := newSpecTestServer(t)
	for _, method := range []string{a2a.MethodSendStreamingMessage, a2a.MethodSubscribeToTask} {
		resp := jsonrpcCall(t, ts, method, struct{}{}, 1)
		require.NotNil(t, resp.Error, "method %s: expected error", method)
		assert.Equal(t, a2a.JSONRPCErrMethodNotFound, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "REST")
	}
}

func TestJSONRPC_MalformedEnvelopeReturnsParseError(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	res := mustPost(t, ts.URL+"/jsonrpc", a2a.ContentTypeSpec, strings.NewReader("{not-json"))
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var resp a2a.JSONRPCResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrParseError, resp.Error.Code)
}

func TestJSONRPC_MissingIDRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	// Send a well-formed envelope minus ID — Validate flags it.
	body := []byte(`{"jsonrpc":"2.0","method":"SendMessage","params":{}}`)
	res := mustPost(t, ts.URL+"/jsonrpc", a2a.ContentTypeSpec, bytes.NewReader(body))
	defer func() { _ = res.Body.Close() }()
	var resp a2a.JSONRPCResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrInvalidRequest, resp.Error.Code)
}

func TestJSONRPC_BadParamsShapeReturnsInvalidParams(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	// Send SendMessage with a params that isn't a Message.
	body := []byte(`{"jsonrpc":"2.0","method":"SendMessage","params":"not-a-message","id":42}`)
	res := mustPost(t, ts.URL+"/jsonrpc", a2a.ContentTypeSpec, bytes.NewReader(body))
	defer func() { _ = res.Body.Close() }()
	var resp a2a.JSONRPCResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, a2a.JSONRPCErrInvalidParams, resp.Error.Code)
}

func TestJSONRPC_AgentCardAdvertisesJSONRPCInterface(t *testing.T) {
	t.Parallel()
	// The card must tell peers that JSON-RPC is available so
	// SDKs that prefer it can find our endpoint.
	ts, _ := newSpecTestServer(t)
	res := mustGet(t, ts.URL+"/.well-known/agent-card.json")
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	require.NoError(t, json.NewDecoder(res.Body).Decode(&card))

	var found bool
	for _, iface := range card.Interfaces {
		if iface.ProtocolBinding == "JSONRPC" {
			found = true
			assert.Contains(t, iface.URL, "/jsonrpc",
				"JSONRPC interface URL should end in /jsonrpc")
		}
	}
	assert.True(t, found, "card.interfaces[] must advertise JSONRPC alongside REST")
}
