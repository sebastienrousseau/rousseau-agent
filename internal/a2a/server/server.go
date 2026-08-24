// Package server implements the A2A server surface: an HTTP endpoint
// that publishes a [a2a.CapabilityCard] and accepts inbound tasks.
//
// # Status
//
// Scaffold only — the type shapes and interface are in place so the
// full wire protocol can be layered in without further refactoring.
// See [docs/a2a.md](../../../docs/a2a.md) for the design.
package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// Handler is the seam an A2A server uses to dispatch inbound tasks
// to whatever domain code should handle them (typically an
// [agent.Agent] Turn on a per-task session). Implementations must be
// safe for concurrent use.
type Handler interface {
	// OnTask fires when a peer submits a new task. The implementation
	// runs the task and streams updates via emit. When streaming is
	// not supported, emit exactly one final update with
	// Status=completed / Status=failed.
	OnTask(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error
}

// Server is the A2A HTTP server. Kept minimal — the full spec's
// authentication + rate-limiting + streaming details land as the
// runtime is built out.
type Server struct {
	Card    a2a.CapabilityCard
	Handler Handler
	// Auth is the bearer-token allowlist (empty disables auth — DO
	// NOT deploy without setting this in production).
	Auth []string
}

// New constructs a Server. Returns an error when Handler is nil.
func New(card a2a.CapabilityCard, h Handler, auth []string) (*Server, error) {
	if h == nil {
		return nil, errors.New("a2a/server: Handler is required")
	}
	return &Server{Card: card, Handler: h, Auth: auth}, nil
}

// Serve blocks until ctx cancels or the listener errors. The address
// syntax is Go's stdlib net.Listen ("host:port").
//
// STATUS: not implemented — currently returns an error immediately.
// The real serve loop will register:
//
//	GET  /.well-known/agent-capabilities  → JSON CapabilityCard
//	POST /tasks                            → accept task, return task_id
//	GET  /tasks/{id}                       → poll status
//	GET  /tasks/{id}/events                → SSE stream of TaskUpdates
//	POST /tasks/{id}/cancel                → cancellation
func (s *Server) Serve(ctx context.Context, addr string) error {
	_ = ctx
	_ = addr
	return errors.New("a2a/server: Serve not implemented — track docs/a2a.md")
}

// mux (unexported) is where the eventual real handlers will be
// registered. Left in place so future PRs can drop routes in without
// having to introduce the type.
type mux struct {
	router *http.ServeMux
}

var _ = (&mux{}).router // silence unused warnings during scaffold phase
