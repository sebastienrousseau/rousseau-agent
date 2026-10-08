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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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
	// SigningKey, when set, causes the v1.0 well-known AgentCard route
	// to sign the response with Ed25519 + JWS per the A2A v1.0.1
	// signatures[] surface. Peers that trust the corresponding
	// public key can then verify card authenticity + integrity.
	// Zero-value skips signing — cards are served unsigned.
	SigningKey ed25519.PrivateKey

	// PublicURL is the operator-configured external base URL
	// (a2a.server.public_url). The well-known AgentCard is signed only
	// when it is set, and then always advertises this URL. Empty
	// serves a request-derived card unsigned: a URL taken from Host /
	// X-Forwarded-Host is attacker-controlled and must never carry
	// the operator's signature.
	PublicURL string
	// TrustedProxies lists the reverse-proxy networks
	// (a2a.server.trusted_proxies) whose X-Forwarded-Proto /
	// X-Forwarded-Host headers are honoured. A request whose
	// RemoteAddr is outside every prefix has those headers ignored.
	TrustedProxies []netip.Prefix
	// Logger receives operational warnings. Nil uses slog.Default.
	Logger *slog.Logger

	// MaxInflightPerPeer caps running tasks per peer. Zero uses 4.
	MaxInflightPerPeer int
	// MaxInflight caps running tasks across all peers. Zero uses 32.
	MaxInflight int

	// TaskRetention is how long a terminal task stays queryable
	// before it is evicted from memory. Zero uses 10 minutes. Without
	// eviction every accepted task (payload plus up to 256 updates)
	// lived for the process lifetime.
	TaskRetention time.Duration

	mu    sync.Mutex
	tasks map[string]*taskState
	// inflight counts running tasks per owner; inflightTotal across
	// all owners. Guarded by mu.
	inflight      map[string]int
	inflightTotal int
	// baseCtx is the Serve context; task handlers derive from it so
	// shutdown cancels them. Nil (tests calling spawnTask directly)
	// falls back to context.Background.
	baseCtx context.Context
	// running joins task goroutines on shutdown.
	running sync.WaitGroup
	// unsignedOnce limits the "card served unsigned" warning to one
	// line per process.
	unsignedOnce sync.Once
}

// logger returns s.Logger or slog.Default.
func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// defaultTaskRetention is the terminal-task eviction delay.
const defaultTaskRetention = 10 * time.Minute

// Default in-flight caps: per authenticated peer and server-wide.
const (
	defaultMaxInflightPerPeer = 4
	defaultMaxInflight        = 32
)

// shutdownJoinTimeout bounds how long Serve waits for running task
// handlers after cancelling them.
const shutdownJoinTimeout = 5 * time.Second

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
	taskCtx, cancelTasks := context.WithCancel(ctx)
	defer cancelTasks()
	s.mu.Lock()
	s.baseCtx = taskCtx
	s.mu.Unlock()

	srv := HTTPServer(s.mux())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownJoinTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort
		<-done
		cancelTasks()
		s.joinTasks(shutdownCtx)
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// HTTPServer returns the http.Server the A2A endpoint is served with:
// header, body and idle deadlines bound slow or parked connections.
// There is deliberately no WriteTimeout — SSE task streams stay open
// for as long as the task runs.
func HTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Router returns the http.Handler that maps the A2A routes so
// operators can embed the endpoints in an existing HTTP server
// (behind their own mux, TLS, etc.) instead of calling Serve.
func (s *Server) Router() http.Handler { return s.mux() }

// mux wires the router. Both the legacy v0-shorthand routes and the
// A2A v1.0.1 spec routes are served — see docs/a2a-conformance.md for
// the deprecation timeline.
//
// The spec's colon-verb routes (e.g. `/tasks/{id}:cancel`) don't fit
// Go's ServeMux wildcard grammar, which forbids mixed literal + wildcard
// segments. We dispatch on the raw segment inside a single handler
// per method (see [Server.handleGetTaskVerb], [Server.handlePostTaskVerb])
// which strips the `:verb` suffix and routes to the right handler,
// falling back to the legacy shape when the segment is a bare id.
func (s *Server) mux() http.Handler {
	m := http.NewServeMux()
	// Legacy v0-shorthand routes (kept for backwards compatibility).
	m.HandleFunc("GET /.well-known/agent-capabilities", s.handleCard)
	m.HandleFunc("POST /tasks", s.authed(s.handleSubmit))
	m.HandleFunc("GET /tasks/{id}/events", s.authed(s.handleEvents))
	m.HandleFunc("POST /tasks/{id}/cancel", s.authed(s.handleCancel))
	// A2A v1.0.1 spec routes.
	m.HandleFunc("GET /.well-known/agent-card.json", s.handleSpecCard)
	m.HandleFunc("POST /message:send", s.authed(s.handleSpecMessageSend))
	// Combined dispatchers — see doc comment above.
	m.HandleFunc("GET /tasks/{spec}", s.authed(s.handleGetTaskVerb))
	m.HandleFunc("POST /tasks/{spec}", s.authed(s.handlePostTaskVerb))
	// A2A JSON-RPC 2.0 binding — single endpoint, method dispatch
	// inside handleJSONRPC.
	m.HandleFunc("POST /jsonrpc", s.authed(s.handleJSONRPC))
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
		writeErr(w, http.StatusBadRequest, s.peerError("invalid task body", err))
		return
	}
	if task.TaskID == "" {
		task.TaskID = newTaskID()
	}
	if task.Prompt == "" && task.SkillName == "" {
		writeErr(w, http.StatusBadRequest, "task must set prompt or skill_name")
		return
	}

	state, err := s.spawnTask(r.Context(), task)
	if err != nil {
		writeErr(w, spawnErrStatus(err), s.spawnErrText(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"task_id": state.id,
		"status":  string(a2a.TaskStatusRunning),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.lookupFor(r.Context(), id)
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
	state := s.lookupFor(r.Context(), id)
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
	tokens := newTokenSet(s.Auth)
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		tok := strings.TrimPrefix(hdr, "Bearer ")
		if !tokens.contains(tok) {
			writeErr(w, http.StatusForbidden, "invalid bearer token")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, PeerID(tok))))
	}
}

// tokenSet is the bearer-token allowlist. It keeps only SHA-256
// digests, so the plaintext tokens are not retained by the middleware,
// and matches a presented token against every entry with
// subtle.ConstantTimeCompare, so neither a map lookup's hashing and
// bucket walk nor an early exit leaks how close a guess came.
type tokenSet struct{ digests [][sha256.Size]byte }

func newTokenSet(tokens []string) tokenSet {
	ds := make([][sha256.Size]byte, 0, len(tokens))
	for _, t := range tokens {
		ds = append(ds, sha256.Sum256([]byte(t)))
	}
	return tokenSet{digests: ds}
}

// contains reports whether tok is in the set. It compares against all
// entries without short-circuiting.
func (ts tokenSet) contains(tok string) bool {
	sum := sha256.Sum256([]byte(tok))
	match := 0
	for i := range ts.digests {
		match |= subtle.ConstantTimeCompare(sum[:], ts.digests[i][:])
	}
	return match == 1
}

// peerKey is the context key for the authenticated peer identity.
type peerKey struct{}

// PeerID derives a stable, non-reversible identity from a bearer
// token: "tok:" plus the first 16 hex chars of its SHA-256. Operators
// bind RBAC and audit rules to this value; it cannot be forged by a
// caller that does not hold the token.
func PeerID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "tok:" + hex.EncodeToString(sum[:8])
}

// peerFromContext returns the authenticated peer id, or "" when the
// server runs without auth.
func peerFromContext(ctx context.Context) string {
	id, _ := ctx.Value(peerKey{}).(string)
	return id
}

// errTaskExists is returned by spawnTask when a client-chosen TaskID
// is already in use. Routes map it to 409 / JSON-RPC -32602.
var errTaskExists = errors.New("task id already exists")

// errTooManyTasks is returned by spawnTask when the peer or the server
// is at its in-flight cap. Routes map it to 429 / JSON-RPC -32029.
var errTooManyTasks = errors.New("too many running tasks; retry later")

// spawnErrStatus maps a spawnTask error to its REST status.
func spawnErrStatus(err error) int {
	switch {
	case errors.Is(err, errTaskExists):
		return http.StatusConflict
	case errors.Is(err, errTooManyTasks):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// spawnTask records a new task, launches its Handler in a goroutine,
// and returns the taskState so the caller can compose the response.
//
// The task's Peer is always taken from ctx (the authenticated request
// context), never from the caller, so no route can omit or forge it.
// The task is owned by that peer: lookupFor hides it from any other.
// A TaskID that is already registered is refused with errTaskExists
// rather than replacing the running task, and a peer (or the server)
// at its in-flight cap is refused with errTooManyTasks.
func (s *Server) spawnTask(ctx context.Context, task a2a.Task) (*taskState, error) {
	task.Peer = peerFromContext(ctx)
	s.mu.Lock()
	base := s.baseCtx
	s.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	taskCtx, cancel := context.WithCancel(base)
	state := &taskState{
		id:     task.TaskID,
		owner:  task.Peer,
		task:   task,
		cancel: cancel,
		status: a2a.TaskStatusRunning,
	}
	if err := s.register(state); err != nil {
		cancel()
		return nil, err
	}

	s.running.Add(1)
	go s.runTask(taskCtx, state)
	return state, nil
}

// register adds state to the task map and takes an in-flight slot for
// its owner, unless the id is taken or a cap is reached.
func (s *Server) register(state *taskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.tasks[state.id]; taken {
		return errTaskExists
	}
	if s.inflightTotal >= capOr(s.MaxInflight, defaultMaxInflight) ||
		s.inflight[state.owner] >= capOr(s.MaxInflightPerPeer, defaultMaxInflightPerPeer) {
		return errTooManyTasks
	}
	if s.inflight == nil {
		s.inflight = make(map[string]int)
	}
	s.inflight[state.owner]++
	s.inflightTotal++
	s.tasks[state.id] = state
	return nil
}

// release returns owner's in-flight slot when its task finishes.
func (s *Server) release(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflightTotal--
	if s.inflight[owner]--; s.inflight[owner] <= 0 {
		delete(s.inflight, owner)
	}
}

// capOr returns n, or def when n is not positive.
func capOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// runTask runs the Handler for one task and guarantees a terminal
// update. Runs on its own goroutine; s.running tracks it.
func (s *Server) runTask(ctx context.Context, state *taskState) {
	defer s.running.Done()
	defer s.scheduleEvict(state.id)
	defer s.release(state.owner)
	emit := func(upd a2a.TaskUpdate) {
		if upd.TaskID == "" {
			upd.TaskID = state.id
		}
		if upd.At.IsZero() {
			upd.At = time.Now().UTC()
		}
		state.emit(upd)
	}
	err := s.Handler.OnTask(ctx, state.task, emit)
	if err != nil && !state.isTerminal() {
		emit(a2a.TaskUpdate{
			Status:      a2a.TaskStatusFailed,
			Message:     s.peerError("task failed", err, slog.String("task_id", state.id)),
			FailureCode: "handler_error",
		})
		return
	}
	// Handler returned nil but never emitted a terminal update —
	// synthesize a completed marker so subscribers unblock.
	if !state.isTerminal() {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
	}
}

// scheduleEvict drops a finished task from the map after
// TaskRetention so peers can still poll its terminal state for a
// while without the server retaining every task forever.
func (s *Server) scheduleEvict(id string) {
	retention := s.TaskRetention
	if retention <= 0 {
		retention = defaultTaskRetention
	}
	time.AfterFunc(retention, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if st, ok := s.tasks[id]; ok && st.isTerminal() {
			delete(s.tasks, id)
		}
	})
}

// joinTasks waits for running task handlers until ctx expires.
func (s *Server) joinTasks(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Server) lookup(id string) *taskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id]
}

// lookupFor returns the task only when the peer in ctx owns it. A
// task owned by another peer is reported exactly like a missing one
// so a peer cannot probe for other peers' task ids.
func (s *Server) lookupFor(ctx context.Context, id string) *taskState {
	state := s.lookup(id)
	if state == nil || state.owner != peerFromContext(ctx) {
		return nil
	}
	return state
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
