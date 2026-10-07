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
	// ModernProtocolVersion is the stateless revision: no initialize
	// handshake, version and capabilities in every request's _meta.
	ModernProtocolVersion = "2026-07-28"
	// LatestLegacyProtocolVersion is the newest revision that still
	// uses the initialize handshake.
	LatestLegacyProtocolVersion = "2025-11-25"
	// jsonRPCVersion is always "2.0" for MCP.
	jsonRPCVersion = "2.0"
)

// LegacyProtocolVersions are the initialize-handshake revisions this
// module speaks, newest first.
var LegacyProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// IsLegacyProtocolVersion reports whether v is one of
// LegacyProtocolVersions.
func IsLegacyProtocolVersion(v string) bool {
	for _, l := range LegacyProtocolVersions {
		if l == v {
			return true
		}
	}
	return false
}

// _meta keys the 2026-07-28 revision requires on every request.
const (
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
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
	// MethodDiscover is the 2026-07-28 replacement for initialize.
	MethodDiscover = "server/discover"
	// MethodCancelled tells the peer a request was abandoned.
	MethodCancelled = "notifications/cancelled"
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
	// CodeMissingClientCapability (2026-07-28): the call needs a
	// capability the client did not declare.
	CodeMissingClientCapability = -32021
	// CodeUnsupportedProtocolVersion (2026-07-28): the server does not
	// speak the requested revision; data.supported lists what it does.
	CodeUnsupportedProtocolVersion = -32022
)

// DiscoverResult is the server/discover response (2026-07-28).
type DiscoverResult struct {
	SupportedVersions []string        `json:"supportedVersions"`
	Capabilities      json.RawMessage `json:"capabilities,omitempty"`
	Instructions      string          `json:"instructions,omitempty"`
}

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

// ToolsListParams is the tools/list request payload. Cursor is the
// previous page's NextCursor; empty asks for the first page.
type ToolsListParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ToolsListResult is the tools/list response. A non-empty NextCursor
// means more tools are available on the next page.
type ToolsListResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
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
