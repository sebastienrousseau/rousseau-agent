// Package mcp implements a small stdio Model Context Protocol server
// (JSON-RPC 2.0 over line-delimited stdio). It is deliberately minimal:
// only the methods rousseau needs to publish its own state to a host
// like Claude Code, Cursor, or Codex.
//
// The wire format is defined by
// https://modelcontextprotocol.io/ and stable enough that this file
// re-implements the small envelope rather than pulling in a third-party
// SDK. When the protocol grows, migrate to the official Go SDK — the
// Server type here isolates the surface that would need swapping.
package mcp

import "encoding/json"

// Protocol identifier constants.
const (
	// ProtocolVersion is the MCP revision this server implements.
	ProtocolVersion = "2024-11-05"
	// jsonRPCVersion is always "2.0" for MCP.
	jsonRPCVersion = "2.0"
)

// Method names published by the server.
const (
	MethodInitialize    = "initialize"
	MethodInitialized   = "notifications/initialized"
	MethodToolsList     = "tools/list"
	MethodToolsCall     = "tools/call"
	MethodResourcesList = "resources/list"
	MethodResourcesRead = "resources/read"
	MethodPromptsList   = "prompts/list"
	MethodShutdown      = "shutdown"
	MethodPing          = "ping"
)

// Envelope is the JSON-RPC 2.0 request / notification / response
// envelope. Fields absent on a given variant are marked omitempty.
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC error codes with rousseau-specific extensions.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeToolNotFound   = -32000
)

// InitializeParams is the payload sent by the host at the start of a
// session. rousseau only inspects the client's protocol version — we
// negotiate down if the host is on an older revision than us.
type InitializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// InitializeResult is the server's response to initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// ServerInfo advertises the server's identity to the host.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities lists the categories the server supports. rousseau
// only exposes tools today; resources / prompts are reserved for
// future features.
type ServerCapabilities struct {
	Tools *ToolCapability `json:"tools,omitempty"`
}

// ToolCapability advertises the tool surface.
type ToolCapability struct {
	// ListChanged is set when the server can emit
	// notifications/tools/list_changed events. rousseau's tool set is
	// static at process start; kept false to avoid over-promising.
	ListChanged bool `json:"listChanged,omitempty"`
}

// Tool is the shape returned by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsListResult is the tools/list response.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolsCallParams is the tools/call request payload.
type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolsCallResult is the tools/call response.
type ToolsCallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content is a single output block returned by a tool.
type Content struct {
	Type string `json:"type"` // "text" is the only kind we emit today
	Text string `json:"text,omitempty"`
}
