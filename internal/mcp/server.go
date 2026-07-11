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
		panic(err)
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
			_ = enc.Encode(errorResponse(nil, CodeParseError, "invalid JSON"))
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

// dispatch routes a single envelope to the appropriate handler.
// Returns nil when the envelope was a notification (no response).
func (s *Server) dispatch(ctx context.Context, env Envelope) *Envelope {
	if env.JSONRPC != jsonRPCVersion {
		return errorResponse(env.ID, CodeInvalidRequest, "expected jsonrpc=2.0")
	}
	switch env.Method {
	case MethodInitialize:
		return s.handleInitialize(env)
	case MethodInitialized:
		// Notification — no reply.
		return nil
	case MethodPing:
		return okResponse(env.ID, struct{}{})
	case MethodToolsList:
		return s.handleToolsList(env)
	case MethodToolsCall:
		return s.handleToolsCall(ctx, env)
	case MethodResourcesList:
		return okResponse(env.ID, map[string]any{"resources": []any{}})
	case MethodPromptsList:
		return okResponse(env.ID, map[string]any{"prompts": []any{}})
	case MethodShutdown:
		return okResponse(env.ID, struct{}{})
	default:
		return errorResponse(env.ID, CodeMethodNotFound, "method not found: "+env.Method)
	}
}

func (s *Server) handleInitialize(env Envelope) *Envelope {
	// The client's protocol version is informational for now — we
	// declare ours and hope negotiation via capabilities is enough.
	return okResponse(env.ID, InitializeResult{
		ProtocolVersion: ProtocolVersion,
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
