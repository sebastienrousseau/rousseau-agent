package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// Handler processes one tool call. Handlers own their arguments'
// unmarshalling and return either a slice of Content blocks or an
// error. Errors are surfaced back to the host as a tool result with
// isError=true — MCP hosts expect tool failures to flow through the
// content channel, not the JSON-RPC error channel.
type Handler func(ctx context.Context, args json.RawMessage) ([]Content, error)

// ToolSpec bundles a tool advertised to the host with the handler that
// serves it. Description and InputSchema are surfaced verbatim to
// tools/list.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
}

// Server is a stdio MCP server. Register tools before Serve.
type Server struct {
	info   ServerInfo
	logger *slog.Logger
	mu     sync.RWMutex
	tools  map[string]ToolSpec
	order  []string // insertion order for deterministic tools/list output
}

// NewServer constructs an empty Server. logger may be nil.
func NewServer(name, version string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		info:   ServerInfo{Name: name, Version: version},
		logger: logger,
		tools:  map[string]ToolSpec{},
	}
}

// Register adds a tool. Duplicate names return an error.
func (s *Server) Register(t ToolSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Name == "" {
		return errors.New("mcp: tool name is required")
	}
	if _, ok := s.tools[t.Name]; ok {
		return fmt.Errorf("mcp: duplicate tool %q", t.Name)
	}
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
	return nil
}

// MustRegister is Register that panics on error. Reserved for
// package-init wiring in main.
func (s *Server) MustRegister(t ToolSpec) {
	if err := s.Register(t); err != nil {
		panic(err) //nolint:forbidigo // documented Must* variant; caller opts into panic-on-misconfiguration
	}
}

// Serve reads JSON-RPC envelopes from r and writes responses to w,
// blocking until r closes or ctx is cancelled. It is safe to invoke
// concurrent Serve calls on independent transports.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			if encErr := enc.Encode(errorResponse(nil, CodeParseError, "invalid JSON")); encErr != nil {
				s.logger.Warn("mcp.encode_error", slog.String("err", encErr.Error()))
			}
			continue
		}
		resp := s.dispatch(ctx, env)
		if resp == nil {
			// Notification — no response required.
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("mcp: write response: %w", err)
		}
	}
	return scanner.Err()
}

// staticResults answers the methods whose result never varies.
var staticResults = map[string]any{
	MethodPing:          struct{}{},
	MethodResourcesList: map[string]any{"resources": []any{}},
	MethodPromptsList:   map[string]any{"prompts": []any{}},
	MethodShutdown:      struct{}{},
}

// dispatch routes a single envelope to the appropriate handler.
// Returns nil when the envelope was a notification (no response). A
// request naming a protocol version in params._meta is a 2026-07-28
// stateless request; anything else is the initialize-era protocol.
func (s *Server) dispatch(ctx context.Context, env Envelope) *Envelope {
	if env.JSONRPC != jsonRPCVersion {
		return errorResponse(env.ID, CodeInvalidRequest, "expected jsonrpc=2.0")
	}
	meta, stateless := requestMeta(env.Params)
	if !stateless {
		return s.route(ctx, env)
	}
	if bad := checkStatelessMeta(env, meta); bad != nil {
		return bad
	}
	return markComplete(s.route(ctx, env), env.Method)
}

// route answers one request or notification by method.
func (s *Server) route(ctx context.Context, env Envelope) *Envelope {
	if result, ok := staticResults[env.Method]; ok {
		return okResponse(env.ID, result)
	}
	switch env.Method {
	case MethodInitialize:
		return s.handleInitialize(env)
	case MethodInitialized:
		// Notification — no reply.
		return nil
	case MethodDiscover:
		return s.handleDiscover(env)
	case MethodToolsList:
		return s.handleToolsList(env)
	case MethodToolsCall:
		return s.handleToolsCall(ctx, env)
	default:
		if len(env.ID) == 0 {
			// Unknown notifications (e.g. notifications/cancelled for a
			// call that already finished) are ignored, not answered.
			return nil
		}
		return errorResponse(env.ID, CodeMethodNotFound, "method not found: "+env.Method)
	}
}

// handleInitialize negotiates the initialize-era revision: the
// client's version is echoed when the server speaks it, otherwise the
// server answers with its newest initialize-era revision and the
// client decides whether to continue.
func (s *Server) handleInitialize(env Envelope) *Envelope {
	var params InitializeParams
	_ = json.Unmarshal(env.Params, &params) //nolint:errcheck // missing or malformed params negotiate the default
	version := LatestLegacyProtocolVersion
	if IsLegacyProtocolVersion(params.ProtocolVersion) {
		version = params.ProtocolVersion
	}
	return okResponse(env.ID, InitializeResult{
		ProtocolVersion: version,
		ServerInfo:      s.info,
		Capabilities: ServerCapabilities{
			Tools: &ToolCapability{ListChanged: false},
		},
	})
}

func (s *Server) handleToolsList(env Envelope) *Envelope {
	s.mu.RLock()
	tools := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		tools = append(tools, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	s.mu.RUnlock()
	return okResponse(env.ID, ToolsListResult{Tools: tools})
}

func (s *Server) handleToolsCall(ctx context.Context, env Envelope) *Envelope {
	var params ToolsCallParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return errorResponse(env.ID, CodeInvalidParams, "cannot decode params: "+err.Error())
	}
	s.mu.RLock()
	spec, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return errorResponse(env.ID, CodeToolNotFound, "unknown tool: "+params.Name)
	}
	content, err := spec.Handler(ctx, params.Arguments)
	if err != nil {
		s.logger.Warn("mcp.tool_error", slog.String("tool", params.Name), slog.String("err", err.Error()))
		return okResponse(env.ID, ToolsCallResult{
			Content: []Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}
	if content == nil {
		content = []Content{}
	}
	return okResponse(env.ID, ToolsCallResult{Content: content})
}

// okResponse builds a Result envelope with the given payload.
func okResponse(id json.RawMessage, result any) *Envelope {
	b, err := json.Marshal(result)
	if err != nil {
		return errorResponse(id, CodeInternalError, "marshal result: "+err.Error())
	}
	return &Envelope{JSONRPC: jsonRPCVersion, ID: id, Result: b}
}

// errorResponse builds an Error envelope with the given code / message.
func errorResponse(id json.RawMessage, code int, msg string) *Envelope {
	return &Envelope{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
}

// TextContent is a small helper for tool handlers that want to return
// a single text block.
func TextContent(text string) []Content {
	return []Content{{Type: "text", Text: text}}
}
