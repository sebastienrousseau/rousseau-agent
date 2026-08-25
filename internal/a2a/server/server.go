// Package server implements the A2A server surface: an HTTP endpoint
// that publishes a [a2a.CapabilityCard] and accepts inbound tasks.
//
// # Wire protocol
//
//	GET  /.well-known/agent-capabilities  → JSON CapabilityCard
//	POST /tasks                            → accept task, return {task_id}
//	GET  /tasks/{id}                       → poll last-known status
//	GET  /tasks/{id}/events                → SSE stream of TaskUpdates
//	POST /tasks/{id}/cancel                → cancel a running task
//
// Bearer-token auth is applied to every route except the capabilities
// card when [Server.Auth] is non-empty. See [docs/a2a.md] for the
// design.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// Handler is the seam an A2A server uses to dispatch inbound tasks to
// domain code (typically an [agent.Agent] Turn on a per-task session).
// Implementations must be safe for concurrent use.
type Handler interface {
	// OnTask fires when a peer submits a new task. The implementation
	// runs the task and streams updates via emit. Non-streaming
	// implementations emit exactly one final update with
	// Status=completed | failed.
	//
	// Returning an error is equivalent to emitting a Status=failed
	// update with the error text as Message.
	OnTask(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error
}

// Server is the A2A HTTP server.
type Server struct {
	Card    a2a.CapabilityCard
	Handler Handler
	// Auth is the bearer-token allowlist. Empty disables auth — DO
	// NOT deploy without setting this in production.
	Auth []string

	mu    sync.Mutex
	tasks map[string]*taskState
}

// New constructs a Server. Returns an error when Handler is nil.
func New(card a2a.CapabilityCard, h Handler, auth []string) (*Server, error) {
	if h == nil {
		return nil, errors.New("a2a/server: Handler is required")
	}
	return &Server{
		Card:    card,
		Handler: h,
		Auth:    auth,
		tasks:   make(map[string]*taskState),
	}, nil
}

// Serve blocks until ctx cancels or the listener errors. The address
// syntax is Go's stdlib net.Listen ("host:port").
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("a2a/server: listen %s: %w", addr, err)
	}
	return s.serveListener(ctx, ln)
}

// ServeListener runs the HTTP loop against an existing listener —
// callers use this to inject an httptest listener under test.
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	return s.serveListener(ctx, ln)
}

func (s *Server) serveListener(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.mux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort
		<-done
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Router returns the http.Handler that maps the A2A routes so
// operators can embed the endpoints in an existing HTTP server
// (behind their own mux, TLS, etc.) instead of calling Serve.
func (s *Server) Router() http.Handler { return s.mux() }

// mux wires the router.
func (s *Server) mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/agent-capabilities", s.handleCard)
	m.HandleFunc("POST /tasks", s.authed(s.handleSubmit))
	m.HandleFunc("GET /tasks/{id}", s.authed(s.handleStatus))
	m.HandleFunc("GET /tasks/{id}/events", s.authed(s.handleEvents))
	m.HandleFunc("POST /tasks/{id}/cancel", s.authed(s.handleCancel))
	return m
}

func (s *Server) handleCard(w http.ResponseWriter, _ *http.Request) {
	card := s.Card
	if card.PublishedAt.IsZero() {
		card.PublishedAt = time.Now().UTC()
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var task a2a.Task
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&task); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid task body: "+err.Error())
		return
	}
	if task.TaskID == "" {
		task.TaskID = newTaskID()
	}
	if task.Prompt == "" && task.SkillName == "" {
		writeErr(w, http.StatusBadRequest, "task must set prompt or skill_name")
		return
	}

	state := s.spawnTask(task)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"task_id": state.id,
		"status":  string(a2a.TaskStatusRunning),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.lookup(id)
	if state == nil {
		writeErr(w, http.StatusNotFound, "unknown task_id")
		return
	}
	writeJSON(w, http.StatusOK, state.snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.lookup(id)
	if state == nil {
		writeErr(w, http.StatusNotFound, "unknown task_id")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "SSE not supported by this ResponseWriter")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := state.subscribe()
	defer cancel()

	// Replay the buffered history first so late subscribers still
	// see everything (including terminal state for tasks that
	// completed before we subscribed).
	for _, upd := range state.history() {
		if err := writeSSE(w, upd); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case upd, alive := <-ch:
			if !alive {
				return
			}
			if err := writeSSE(w, upd); err != nil {
				return
			}
			flusher.Flush()
			if isTerminal(upd.Status) {
				return
			}
		}
	}
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.lookup(id)
	if state == nil {
		writeErr(w, http.StatusNotFound, "unknown task_id")
		return
	}
	state.cancel()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"task_id": id,
		"status":  string(a2a.TaskStatusCancelled),
	})
}

// authed wraps a handler with bearer-token auth when s.Auth is set.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	if len(s.Auth) == 0 {
		return h
	}
	tokens := make(map[string]struct{}, len(s.Auth))
	for _, t := range s.Auth {
		tokens[t] = struct{}{}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if _, ok := tokens[strings.TrimPrefix(hdr, "Bearer ")]; !ok {
			writeErr(w, http.StatusForbidden, "invalid bearer token")
			return
		}
		h(w, r)
	}
}

// spawnTask records a new task, launches its Handler in a goroutine,
// and returns the taskState so the caller can compose the response.
func (s *Server) spawnTask(task a2a.Task) *taskState {
	ctx, cancel := context.WithCancel(context.Background())
	state := &taskState{
		id:     task.TaskID,
		task:   task,
		cancel: cancel,
		status: a2a.TaskStatusRunning,
	}
	s.mu.Lock()
	s.tasks[state.id] = state
	s.mu.Unlock()

	go func() {
		emit := func(upd a2a.TaskUpdate) {
			if upd.TaskID == "" {
				upd.TaskID = state.id
			}
			if upd.At.IsZero() {
				upd.At = time.Now().UTC()
			}
			state.emit(upd)
		}
		err := s.Handler.OnTask(ctx, task, emit)
		if err != nil && !state.isTerminal() {
			emit(a2a.TaskUpdate{
				Status:      a2a.TaskStatusFailed,
				Message:     err.Error(),
				FailureCode: "handler_error",
			})
			return
		}
		// Handler returned nil but never emitted a terminal update —
		// synthesize a completed marker so subscribers unblock.
		if !state.isTerminal() {
			emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
		}
	}()

	return state
}

func (s *Server) lookup(id string) *taskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id]
}

// ---- helpers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // client-closed conn is not our problem
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeSSE(w io.Writer, upd a2a.TaskUpdate) error {
	blob, err := json.Marshal(upd)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", blob)
	return err
}

// randRead is crypto/rand.Read behind a var so tests can exercise the
// entropy-failure fallback in newTaskID, which is otherwise unreachable
// (crypto/rand.Read never returns an error on supported platforms).
var randRead = rand.Read

func newTaskID() string {
	var buf [16]byte
	if _, err := randRead(buf[:]); err != nil {
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return "task-" + hex.EncodeToString(buf[:])
}

func isTerminal(s a2a.TaskStatus) bool {
	switch s {
	case a2a.TaskStatusCompleted, a2a.TaskStatusFailed, a2a.TaskStatusCancelled:
		return true
	default:
		return false
	}
}
