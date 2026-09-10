package client

// Client-side JSON-RPC 2.0 helpers for the A2A binding. See
// internal/a2a/jsonrpc.go for the wire types and
// internal/a2a/server/jsonrpc_handler.go for the server-side
// dispatcher.
//
// Public surface:
//
//	Client.CallJSONRPC(ctx, method, params) (result json.RawMessage, err error)
//
// Callers unmarshal the raw result into the method-specific shape.
// The method-specific convenience wrappers (SendMessageJSONRPC etc.)
// wrap CallJSONRPC and unmarshal for the common cases.
//
// Design note: the client keeps the CallJSONRPC surface generic
// because the JSON-RPC binding is intentionally the second
// transport — most rousseau code paths hit REST first
// (SendMessage/SubscribeToTask). CallJSONRPC exists for interop with
// peers that only speak JSON-RPC (the canonical Python SDK's default
// today).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// jsonrpcCounter mints monotonically-increasing request ids so
// callers don't have to. Kept package-scoped so a single daemon's
// outbound JSON-RPC calls have a globally-unique id space.
var jsonrpcCounter atomic.Uint64

// CallJSONRPC issues a JSON-RPC 2.0 call at the peer's /jsonrpc
// endpoint. Returns the raw result bytes on success. On protocol
// error (envelope.Error != nil) returns an error wrapping the
// JSON-RPC error code + message.
func (c *Client) CallJSONRPC(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("a2a/client: marshal params: %w", err)
	}
	id := jsonrpcCounter.Add(1)
	idRaw, _ := json.Marshal(id) //nolint:errcheck // marshaling uint64 cannot fail
	req := a2a.JSONRPCRequest{
		JSONRPC: a2a.JSONRPCVersion,
		Method:  method,
		Params:  paramsRaw,
		ID:      idRaw,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("a2a/client: marshal envelope: %w", err)
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", a2a.ContentTypeSpec)
	httpReq.Header.Set("Accept", a2a.ContentTypeSpec)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, unexpectedStatus(resp)
	}
	var envelope a2a.JSONRPCResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("a2a/client: decode envelope: %w", err)
	}
	if envelope.Error != nil {
		return nil, &JSONRPCError{
			Code:    envelope.Error.Code,
			Message: envelope.Error.Message,
		}
	}
	if !bytes.Equal(envelope.ID, idRaw) {
		return nil, fmt.Errorf("a2a/client: response id %s != request id %s", envelope.ID, idRaw)
	}
	return envelope.Result, nil
}

// SendMessageJSONRPC is a typed wrapper for the SendMessage method.
// Callers preferring the JSON-RPC binding over REST use this instead
// of [Client.SendMessage] — the underlying task-spawning path on the
// server is identical.
func (c *Client) SendMessageJSONRPC(ctx context.Context, msg a2a.Message) (a2a.SpecTask, error) {
	if len(msg.Parts) == 0 {
		return a2a.SpecTask{}, errors.New("a2a/client: message.parts is empty")
	}
	raw, err := c.CallJSONRPC(ctx, a2a.MethodSendMessage, msg)
	if err != nil {
		return a2a.SpecTask{}, err
	}
	var task a2a.SpecTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return a2a.SpecTask{}, fmt.Errorf("a2a/client: decode task: %w", err)
	}
	return task, nil
}

// GetTaskJSONRPC is a typed wrapper for the GetTask method.
func (c *Client) GetTaskJSONRPC(ctx context.Context, taskID string) (a2a.SpecTask, error) {
	if taskID == "" {
		return a2a.SpecTask{}, errors.New("a2a/client: taskID is required")
	}
	raw, err := c.CallJSONRPC(ctx, a2a.MethodGetTask, map[string]string{"id": taskID})
	if err != nil {
		return a2a.SpecTask{}, err
	}
	var task a2a.SpecTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return a2a.SpecTask{}, fmt.Errorf("a2a/client: decode task: %w", err)
	}
	return task, nil
}

// CancelTaskJSONRPC is a typed wrapper for the CancelTask method.
func (c *Client) CancelTaskJSONRPC(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("a2a/client: taskID is required")
	}
	_, err := c.CallJSONRPC(ctx, a2a.MethodCancelTask, map[string]string{"id": taskID})
	return err
}

// JSONRPCError wraps a JSON-RPC 2.0 protocol error surfaced to the
// caller. Kept as a typed error so callers can errors.As against it
// and read Code + Message for retry decisions (e.g. TaskNotFound
// = -32001 → don't retry).
type JSONRPCError struct {
	Code    int
	Message string
}

// Error satisfies error.
func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("a2a/client: JSON-RPC error %d: %s", e.Code, e.Message)
}

// Is enables errors.Is(err, &JSONRPCError{Code: N}) — comparing by
// code alone so callers can match "any TaskNotFound" without
// spelling out the message.
func (e *JSONRPCError) Is(target error) bool {
	other, ok := target.(*JSONRPCError)
	if !ok {
		return false
	}
	return e.Code == other.Code
}
