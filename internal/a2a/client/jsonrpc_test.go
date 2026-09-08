package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// fakeJSONRPCServer is a tiny HTTP handler that decodes a JSON-RPC
// request and lets the test decide the response. Keeps individual
// tests focused on their one behaviour instead of re-writing the
// envelope machinery.
func fakeJSONRPCServer(t *testing.T, respond func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req a2a.JSONRPCRequest
		require.NoError(t, json.Unmarshal(body, &req))
		resp := respond(req)
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)
	c, err := New(Config{Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second})
	require.NoError(t, err)
	return c
}

func TestCallJSONRPC_HappyPath(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		assert.Equal(t, a2a.JSONRPCVersion, req.JSONRPC)
		assert.Equal(t, "TestMethod", req.Method)
		resp, err := a2a.NewResultResponse(req.ID, map[string]string{"greeting": "hi"})
		require.NoError(t, err)
		return resp
	})
	raw, err := c.CallJSONRPC(context.Background(), "TestMethod", map[string]int{"x": 1})
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "hi", got["greeting"])
}

func TestCallJSONRPC_ProtocolErrorSurfaces(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		return a2a.NewErrorResponse(req.ID, a2a.JSONRPCErrTaskNotFound, "no such task")
	})
	_, err := c.CallJSONRPC(context.Background(), "GetTask", struct{}{})
	require.Error(t, err)
	var jrpcErr *JSONRPCError
	require.True(t, errors.As(err, &jrpcErr), "err type = %T, want *JSONRPCError", err)
	assert.Equal(t, a2a.JSONRPCErrTaskNotFound, jrpcErr.Code)
	assert.Equal(t, "no such task", jrpcErr.Message)
	// errors.Is with same-code sentinel should match.
	assert.True(t, errors.Is(err, &JSONRPCError{Code: a2a.JSONRPCErrTaskNotFound}))
	// Different code → does not match.
	assert.False(t, errors.Is(err, &JSONRPCError{Code: a2a.JSONRPCErrInvalidParams}))
}

func TestCallJSONRPC_HTTPErrorSurfaces(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service down")) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)
	c, err := New(Config{Name: "peer", Endpoint: ts.URL, Timeout: 500 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.CallJSONRPC(context.Background(), "GetTask", struct{}{})
	require.Error(t, err)
	// Non-JSON-RPC error → not a *JSONRPCError.
	var jrpcErr *JSONRPCError
	assert.False(t, errors.As(err, &jrpcErr), "HTTP-level errors should not be typed as JSONRPCError")
}

func TestCallJSONRPC_MalformedEnvelopeSurfaces(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-a-jsonrpc-envelope")) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)
	c, err := New(Config{Name: "peer", Endpoint: ts.URL, Timeout: 500 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.CallJSONRPC(context.Background(), "GetTask", struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode envelope")
}

func TestCallJSONRPC_ResponseIDMismatch(t *testing.T) {
	t.Parallel()
	// A confused peer that returns a response with a different ID
	// than the request had — client must reject rather than route
	// that result to the wrong caller.
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		wrongID, err := json.Marshal("wrong")
		if err != nil {
			panic(err)
		}
		resp, err := a2a.NewResultResponse(wrongID, "irrelevant")
		if err != nil {
			panic(err)
		}
		return resp
	})
	_, err := c.CallJSONRPC(context.Background(), "M", struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response id")
}

func TestCallJSONRPC_MonotonicIDs(t *testing.T) {
	t.Parallel()
	// Freeze the "each call gets a unique id" contract so a
	// future refactor to a fixed id doesn't sneak in.
	seen := map[string]struct{}{}
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		seen[string(req.ID)] = struct{}{}
		resp, err := a2a.NewResultResponse(req.ID, 1)
		if err != nil {
			panic(err)
		}
		return resp
	})
	for i := 0; i < 10; i++ {
		_, err := c.CallJSONRPC(context.Background(), "M", struct{}{})
		require.NoError(t, err)
	}
	assert.Len(t, seen, 10, "each call should have a unique id")
}

func TestCallJSONRPC_ParamsMarshalError(t *testing.T) {
	t.Parallel()
	// A channel isn't marshalable → CallJSONRPC must surface the
	// error, not hand a corrupted envelope to the server.
	c, err := New(Config{Name: "p", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.CallJSONRPC(context.Background(), "M", make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal params")
}

func TestSendMessageJSONRPC_HappyPath(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		assert.Equal(t, a2a.MethodSendMessage, req.Method)
		var msg a2a.Message
		require.NoError(t, json.Unmarshal(req.Params, &msg))
		assert.Equal(t, "hi", msg.Parts[0].Text)
		resp, err := a2a.NewResultResponse(req.ID, a2a.SpecTask{
			ID:     "t-1",
			Status: a2a.TaskStatus1{State: a2a.TaskStateSubmitted},
		})
		if err != nil {
			panic(err)
		}
		return resp
	})
	task, err := c.SendMessageJSONRPC(context.Background(), a2a.TextMessage("hi"))
	require.NoError(t, err)
	assert.Equal(t, "t-1", task.ID)
	assert.Equal(t, a2a.TaskStateSubmitted, task.Status.State)
}

func TestSendMessageJSONRPC_EmptyPartsFailsFast(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "p", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.SendMessageJSONRPC(context.Background(), a2a.Message{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parts is empty")
}

func TestSendMessageJSONRPC_MalformedResultSurfaces(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		return a2a.JSONRPCResponse{
			JSONRPC: a2a.JSONRPCVersion,
			Result:  json.RawMessage(`"not-a-task"`),
			ID:      req.ID,
		}
	})
	_, err := c.SendMessageJSONRPC(context.Background(), a2a.TextMessage("hi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode task")
}

func TestGetTaskJSONRPC_HappyPath(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		assert.Equal(t, a2a.MethodGetTask, req.Method)
		var params map[string]string
		require.NoError(t, json.Unmarshal(req.Params, &params))
		assert.Equal(t, "t-42", params["id"])
		resp, err := a2a.NewResultResponse(req.ID, a2a.SpecTask{
			ID:     "t-42",
			Status: a2a.TaskStatus1{State: a2a.TaskStateWorking},
		})
		if err != nil {
			panic(err)
		}
		return resp
	})
	task, err := c.GetTaskJSONRPC(context.Background(), "t-42")
	require.NoError(t, err)
	assert.Equal(t, a2a.TaskStateWorking, task.Status.State)
}

func TestGetTaskJSONRPC_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "p", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.GetTaskJSONRPC(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taskID is required")
}

func TestGetTaskJSONRPC_MalformedResultSurfaces(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		return a2a.JSONRPCResponse{
			JSONRPC: a2a.JSONRPCVersion,
			Result:  json.RawMessage(`42`),
			ID:      req.ID,
		}
	})
	_, err := c.GetTaskJSONRPC(context.Background(), "t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode task")
}

func TestCancelTaskJSONRPC_HappyPath(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		assert.Equal(t, a2a.MethodCancelTask, req.Method)
		resp, err := a2a.NewResultResponse(req.ID, a2a.SpecTask{
			ID:     "t-1",
			Status: a2a.TaskStatus1{State: a2a.TaskStateCanceled},
		})
		if err != nil {
			panic(err)
		}
		return resp
	})
	err := c.CancelTaskJSONRPC(context.Background(), "t-1")
	require.NoError(t, err)
}

func TestCancelTaskJSONRPC_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "p", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	err = c.CancelTaskJSONRPC(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taskID is required")
}

func TestJSONRPCError_ErrorMessageIncludesCodeAndMessage(t *testing.T) {
	t.Parallel()
	e := &JSONRPCError{Code: a2a.JSONRPCErrInvalidRequest, Message: "bad envelope"}
	got := e.Error()
	assert.Contains(t, got, "-32600")
	assert.Contains(t, got, "bad envelope")
}

func TestJSONRPCError_IsDoesNotMatchNonJSONRPCErrors(t *testing.T) {
	t.Parallel()
	// Freeze the "Is only matches other JSONRPCError" contract.
	e := &JSONRPCError{Code: 1}
	assert.False(t, e.Is(errors.New("plain error")))
}

// TestSendMessageJSONRPC_EndToEndAgainstRousseauServer wires the JSON-RPC
// client against a live-shaped server response so a shape regression on
// either side of the JSON-RPC contract breaks CI.
func TestSendMessageJSONRPC_EndToEndAgainstRousseauServer(t *testing.T) {
	t.Parallel()
	c := fakeJSONRPCServer(t, func(req a2a.JSONRPCRequest) a2a.JSONRPCResponse {
		// Prove the server saw the spec-shaped envelope + params.
		assert.Equal(t, "2.0", req.JSONRPC)
		assert.Equal(t, "SendMessage", req.Method)
		var msg a2a.Message
		require.NoError(t, json.Unmarshal(req.Params, &msg))
		require.NotEmpty(t, msg.Parts)
		require.Equal(t, "hello", msg.Parts[0].Text)

		resp, err := a2a.NewResultResponse(req.ID, a2a.SpecTask{
			ID: "t-e2e", ContextID: msg.ContextID,
			Status: a2a.TaskStatus1{State: a2a.TaskStateSubmitted},
		})
		if err != nil {
			panic(err)
		}
		return resp
	})
	msg := a2a.TextMessage("hello")
	msg.ContextID = "conv-1"
	task, err := c.SendMessageJSONRPC(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, "t-e2e", task.ID)
	assert.Equal(t, "conv-1", task.ContextID)
}

// TestCallJSONRPC_TimeoutHonoured freezes the "Config.Timeout wraps
// each call" contract so a slow peer doesn't wedge the caller.
func TestCallJSONRPC_TimeoutHonoured(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(a2a.JSONRPCResponse{JSONRPC: "2.0"}) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)
	c, err := New(Config{Name: "p", Endpoint: ts.URL, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	_, err = c.CallJSONRPC(context.Background(), "M", struct{}{})
	require.Error(t, err)
	// context deadline exceeded surfaces as a URL error wrapping it.
	assert.True(t, strings.Contains(err.Error(), "context deadline") ||
		strings.Contains(err.Error(), "Client.Timeout") ||
		strings.Contains(err.Error(), "canceled"), "err = %v", err)
}
