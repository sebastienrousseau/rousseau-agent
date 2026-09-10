package a2a

// JSON-RPC 2.0 wire types for the A2A protocol's JSON-RPC binding
// (spec §9). The REST binding (spec §11) shipped in v0.0.4 and is the
// primary transport rousseau serves; this file adds the JSON-RPC
// binding as an interop convenience for peers built against the
// canonical Python / TypeScript SDKs.
//
// A2A JSON-RPC methods (spec §9):
//
//	SendMessage                  → non-streaming Task submission
//	SendStreamingMessage         → SSE stream of StreamResponse frames
//	GetTask                      → single-task fetch
//	CancelTask                   → terminate a running task
//	SubscribeToTask              → SSE re-subscribe
//	GetExtendedAgentCard         → authenticated card variant
//	Create/Get/List/Delete-TaskPushNotificationConfig
//
// This file defines the envelope types; server-side dispatch lives in
// internal/a2a/server/jsonrpc_handler.go, client-side call helpers in
// internal/a2a/client/jsonrpc.go.

import (
	"encoding/json"
	"errors"
)

// JSONRPCVersion is the exact value of the "jsonrpc" field per the
// JSON-RPC 2.0 spec.
const JSONRPCVersion = "2.0"

// A2A JSON-RPC method names, kept as constants so a rename is a
// deliberate cross-package decision.
const (
	MethodSendMessage          = "SendMessage"
	MethodSendStreamingMessage = "SendStreamingMessage"
	MethodGetTask              = "GetTask"
	MethodCancelTask           = "CancelTask"
	MethodSubscribeToTask      = "SubscribeToTask"
)

// JSONRPCRequest is one request envelope. `id` is any JSON value per
// the spec — string or integer both work — so it's held as raw JSON.
// Notification-style requests (id absent) are not supported by A2A;
// the server rejects them with -32600.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// JSONRPCResponse is one response envelope. Exactly one of Result and
// Error is populated per the spec; the JSON output honours that via
// omitempty on both.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// JSONRPCError is the standard error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes (spec §5.1).
const (
	// JSONRPCErrParseError is returned when the server can't parse
	// the request body as JSON at all.
	JSONRPCErrParseError = -32700
	// JSONRPCErrInvalidRequest is returned when the parse succeeded
	// but the envelope is malformed (wrong jsonrpc, missing method).
	JSONRPCErrInvalidRequest = -32600
	// JSONRPCErrMethodNotFound is returned when method is unknown.
	JSONRPCErrMethodNotFound = -32601
	// JSONRPCErrInvalidParams is returned when the method exists
	// but its parameters don't match the expected shape.
	JSONRPCErrInvalidParams = -32602
	// JSONRPCErrInternal is returned for any server-side failure
	// that isn't attributable to the caller's request.
	JSONRPCErrInternal = -32603
)

// A2A-specific JSON-RPC error codes (spec §9.5). Uses the -32001..-32099
// server-reserved range per JSON-RPC convention.
const (
	// JSONRPCErrTaskNotFound is returned when the referenced task
	// id doesn't map to a live task.
	JSONRPCErrTaskNotFound = -32001
	// JSONRPCErrTaskNotCancelable is returned when the task exists
	// but is already in a terminal state.
	JSONRPCErrTaskNotCancelable = -32002
	// JSONRPCErrContentTypeNotSupported is returned when the peer
	// sends parts[] the agent can't consume.
	JSONRPCErrContentTypeNotSupported = -32005
)

// NewErrorResponse builds a JSON-RPC error response. Copies the caller
// id verbatim so the peer can correlate the response.
func NewErrorResponse(id json.RawMessage, code int, message string) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		Error:   &JSONRPCError{Code: code, Message: message},
		ID:      id,
	}
}

// NewResultResponse builds a success response with the given result
// payload. The caller marshals its own result — this keeps the
// method-specific types out of this file.
func NewResultResponse(id json.RawMessage, result any) (JSONRPCResponse, error) {
	blob, err := json.Marshal(result)
	if err != nil {
		return JSONRPCResponse{}, err
	}
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		Result:  blob,
		ID:      id,
	}, nil
}

// Validate performs the envelope-level checks the JSON-RPC 2.0
// spec mandates BEFORE dispatching to a handler.
func (r JSONRPCRequest) Validate() error {
	if r.JSONRPC != JSONRPCVersion {
		return errors.New("jsonrpc field must be \"2.0\"")
	}
	if r.Method == "" {
		return errors.New("method is required")
	}
	if len(r.ID) == 0 {
		return errors.New("id is required (notification-style requests are not supported)")
	}
	return nil
}
