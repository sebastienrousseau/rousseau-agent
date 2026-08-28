package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// ---- helpers ---------------------------------------------------------

// handlerFunc adapts a closure to the Handler interface.
type handlerFunc func(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error

func (f handlerFunc) OnTask(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	return f(ctx, task, emit)
}

// noopHandler never emits anything and returns immediately.
func noopHandler() Handler {
	return handlerFunc(func(context.Context, a2a.Task, func(a2a.TaskUpdate)) error { return nil })
}

// blockingHandler stays running until the returned release channel is
// closed, so tests can observe a task in the running state.
func blockingHandler(t *testing.T) (Handler, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "started"})
		select {
		case <-release:
		case <-ctx.Done():
			emit(a2a.TaskUpdate{Status: a2a.TaskStatusCancelled, Message: "ctx cancelled"})
		case <-time.After(10 * time.Second):
		}
		return nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return h, release
}

func newServer(t *testing.T, h Handler, auth []string) *Server {
	t.Helper()
	s, err := New(a2a.CapabilityCard{
		AgentID: "agent-1",
		Name:    "test-agent",
		Version: "v1.2.3",
		Skills:  []a2a.SkillDescriptor{{Name: "review", Description: "reviews code"}},
	}, h, auth)
	require.NoError(t, err)
	return s
}

// errWriter is a ResponseWriter whose body writes always fail. It
// implements http.Flusher so it exercises the SSE path rather than the
// "SSE not supported" branch.
type errWriter struct {
	hdr     http.Header
	status  int
	flushed int
}

func newErrWriter() *errWriter { return &errWriter{hdr: make(http.Header)} }

func (e *errWriter) Header() http.Header       { return e.hdr }
func (e *errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (e *errWriter) WriteHeader(code int)      { e.status = code }
func (e *errWriter) Flush()                    { e.flushed++ }

// plainWriter is a ResponseWriter that deliberately does NOT implement
// http.Flusher, exercising the SSE-unsupported branch.
type plainWriter struct {
	hdr    http.Header
	status int
	body   strings.Builder
}

func newPlainWriter() *plainWriter { return &plainWriter{hdr: make(http.Header)} }

func (p *plainWriter) Header() http.Header { return p.hdr }
func (p *plainWriter) Write(b []byte) (int, error) {
	return p.body.Write(b)
}
func (p *plainWriter) WriteHeader(code int) { p.status = code }

// ---- New -------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Run("nil handler is rejected", func(t *testing.T) {
		s, err := New(a2a.CapabilityCard{}, nil, nil)
		require.Error(t, err)
		assert.Nil(t, s)
		assert.Contains(t, err.Error(), "Handler is required")
	})

	t.Run("valid handler", func(t *testing.T) {
		h := noopHandler()
		s, err := New(a2a.CapabilityCard{AgentID: "x"}, h, []string{"tok"})
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.Equal(t, "x", s.Card.AgentID)
		assert.Equal(t, []string{"tok"}, s.Auth)
		assert.NotNil(t, s.tasks)
	})
}

// ---- Serve / ServeListener -------------------------------------------

func TestServe_ListenError(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	err := s.Serve(context.Background(), "127.0.0.1:-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a2a/server: listen")
}

func TestServe_ShutsDownOnContextCancel(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, "127.0.0.1:0") }()

	// Give the listener a moment to come up, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}

func TestServeListener_ServesRequestsAndStopsOnCancel(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.ServeListener(ctx, ln) }()

	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/.well-known/agent-capabilities") //nolint:noctx // test
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return after context cancel")
	}
}

func TestServeListener_ReturnsListenerError(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() { errCh <- s.ServeListener(context.Background(), ln) }()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, ln.Close())

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.False(t, errors.Is(err, http.ErrServerClosed))
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return after listener close")
	}
}

// ---- capability card -------------------------------------------------

func TestHandleCard(t *testing.T) {
	published := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		card    a2a.CapabilityCard
		assertF func(t *testing.T, got a2a.CapabilityCard)
	}{
		{
			name: "zero PublishedAt is filled in",
			card: a2a.CapabilityCard{AgentID: "a", Name: "n", Version: "v"},
			assertF: func(t *testing.T, got a2a.CapabilityCard) {
				t.Helper()
				assert.False(t, got.PublishedAt.IsZero())
			},
		},
		{
			name: "explicit PublishedAt is preserved",
			card: a2a.CapabilityCard{AgentID: "a", PublishedAt: published, SupportsStreaming: true},
			assertF: func(t *testing.T, got a2a.CapabilityCard) {
				t.Helper()
				assert.True(t, published.Equal(got.PublishedAt))
				assert.True(t, got.SupportsStreaming)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(tc.card, noopHandler(), []string{"secret"})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-capabilities", nil)
			// No Authorization header: the card is deliberately public.
			s.Router().ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			var got a2a.CapabilityCard
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.card.AgentID, got.AgentID)
			tc.assertF(t, got)
		})
	}

	t.Run("server card is not mutated", func(t *testing.T) {
		s := newServer(t, noopHandler(), nil)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-capabilities", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, s.Card.PublishedAt.IsZero(), "handler must not mutate Server.Card")
	})
}

// ---- submit ----------------------------------------------------------

func TestHandleSubmit(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantErrSub string
	}{
		{
			name:       "malformed JSON",
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
			wantErrSub: "invalid task body",
		},
		{
			name:       "empty prompt and skill",
			body:       `{"from_agent":"peer"}`,
			wantStatus: http.StatusBadRequest,
			wantErrSub: "task must set prompt or skill_name",
		},
		{
			name:       "prompt only is accepted",
			body:       `{"prompt":"do the thing"}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "skill only is accepted",
			body:       `{"skill_name":"review"}`,
			wantStatus: http.StatusAccepted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, noopHandler(), nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(tc.body))
			s.Router().ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			var body map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tc.wantErrSub != "" {
				assert.Contains(t, body["error"], tc.wantErrSub)
				return
			}
			assert.NotEmpty(t, body["task_id"])
			assert.True(t, strings.HasPrefix(body["task_id"], "task-"), "server should mint an id")
			assert.Equal(t, string(a2a.TaskStatusRunning), body["status"])
		})
	}

	t.Run("caller-supplied task_id is honoured", func(t *testing.T) {
		h, release := blockingHandler(t)
		s := newServer(t, h, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"task_id":"caller-id","prompt":"hi"}`))
		s.Router().ServeHTTP(rec, req)

		require.Equal(t, http.StatusAccepted, rec.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "caller-id", body["task_id"])
		require.NotNil(t, s.lookup("caller-id"))
		close(release)
	})

	t.Run("oversized body is rejected", func(t *testing.T) {
		s := newServer(t, noopHandler(), nil)
		// A JSON string longer than the 1 MiB limit is truncated mid-token
		// by io.LimitReader, so decoding fails.
		big := `{"prompt":"` + strings.Repeat("a", (1<<20)+16) + `"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(big))
		s.Router().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// ---- status ----------------------------------------------------------

func TestHandleStatus(t *testing.T) {
	t.Run("unknown task", func(t *testing.T) {
		s := newServer(t, noopHandler(), nil)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/nope", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "unknown task_id")
	})

	t.Run("known task reports its snapshot", func(t *testing.T) {
		done := make(chan struct{})
		h := handlerFunc(func(context.Context, a2a.Task, func(a2a.TaskUpdate)) error {
			defer close(done)
			return nil
		})
		s := newServer(t, h, nil)
		state := s.spawnTask(a2a.Task{TaskID: "t1", Prompt: "p"})
		<-done
		waitFor(t, state.isTerminal)

		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/t1", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var snap snapshot
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
		assert.Equal(t, "t1", snap.TaskID)
		assert.Equal(t, a2a.TaskStatusCompleted, snap.Status)
		assert.Equal(t, 1, snap.NumUpdates)
		assert.Equal(t, "t1", snap.Last.TaskID)
		assert.False(t, snap.Last.At.IsZero(), "emit should stamp At")
	})
}

// ---- cancel ----------------------------------------------------------

func TestHandleCancel(t *testing.T) {
	t.Run("unknown task", func(t *testing.T) {
		s := newServer(t, noopHandler(), nil)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks/ghost/cancel", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "unknown task_id")
	})

	t.Run("cancel propagates to the handler context", func(t *testing.T) {
		h, _ := blockingHandler(t)
		s := newServer(t, h, nil)
		s.spawnTask(a2a.Task{TaskID: "t-cancel", Prompt: "p"})

		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks/t-cancel/cancel", nil))
		require.Equal(t, http.StatusAccepted, rec.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "t-cancel", body["task_id"])
		assert.Equal(t, string(a2a.TaskStatusCancelled), body["status"])

		state := s.lookup("t-cancel")
		require.NotNil(t, state)
		waitFor(t, func() bool { return state.snapshot().Status == a2a.TaskStatusCancelled })
	})
}

// ---- auth ------------------------------------------------------------

func TestAuth(t *testing.T) {
	tests := []struct {
		name       string
		auth       []string
		header     string
		wantStatus int
		wantBody   string
	}{
		{name: "no auth configured allows anonymous", auth: nil, header: "", wantStatus: http.StatusAccepted},
		{name: "missing header", auth: []string{"good"}, header: "", wantStatus: http.StatusUnauthorized, wantBody: "missing bearer token"},
		{name: "non-bearer scheme", auth: []string{"good"}, header: "Basic abc", wantStatus: http.StatusUnauthorized, wantBody: "missing bearer token"},
		{name: "wrong token", auth: []string{"good"}, header: "Bearer bad", wantStatus: http.StatusForbidden, wantBody: "invalid bearer token"},
		{name: "correct token", auth: []string{"other", "good"}, header: "Bearer good", wantStatus: http.StatusAccepted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, noopHandler(), tc.auth)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"prompt":"hi"}`))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			s.Router().ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantBody != "" {
				assert.Contains(t, rec.Body.String(), tc.wantBody)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}

	t.Run("auth applies to every non-card route", func(t *testing.T) {
		s := newServer(t, noopHandler(), []string{"good"})
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/tasks/abc"},
			{http.MethodGet, "/tasks/abc/events"},
			{http.MethodPost, "/tasks/abc/cancel"},
		} {
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
			assert.Equal(t, http.StatusUnauthorized, rec.Code, route.path)
		}
		// The card stays public.
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-capabilities", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// ---- SSE events ------------------------------------------------------

func TestHandleEvents_UnknownTask(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/ghost/events", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown task_id")
}

func TestHandleEvents_NonFlusherWriter(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	s.spawnTask(a2a.Task{TaskID: "t-noflush", Prompt: "p"})

	w := newPlainWriter()
	req := httptest.NewRequest(http.MethodGet, "/tasks/t-noflush/events", nil)
	req.SetPathValue("id", "t-noflush")
	s.handleEvents(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.status)
	assert.Contains(t, w.body.String(), "SSE not supported")
}

func TestHandleEvents_ReplaysHistoryThenStreamsToTerminal(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h := handlerFunc(func(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "first", Progress: 0.25})
		close(started)
		<-release
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "done"})
		return nil
	})
	s := newServer(t, h, nil)
	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"task_id":"t-sse","prompt":"go"}`))
	s.Router().ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	<-started

	resp, err := http.Get(ts.URL + "/tasks/t-sse/events") //nolint:noctx // test
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"))

	// Wait until the subscriber is registered before unblocking, so the
	// terminal update arrives over the live channel rather than history.
	state := s.lookup("t-sse")
	require.NotNil(t, state)
	waitFor(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.subscribers) == 1
	})
	close(release)

	updates := readSSE(t, resp)
	require.Len(t, updates, 2)
	assert.Equal(t, a2a.TaskStatusRunning, updates[0].Status)
	assert.Equal(t, "first", updates[0].Message)
	assert.Equal(t, "t-sse", updates[0].TaskID)
	assert.Equal(t, a2a.TaskStatusCompleted, updates[1].Status)
	assert.Equal(t, "done", updates[1].OutputText)
}

func TestHandleEvents_LateSubscriberGetsHistoryOnly(t *testing.T) {
	done := make(chan struct{})
	h := handlerFunc(func(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		defer close(done)
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning})
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusFailed, Message: "boom", FailureCode: "x"})
		return nil
	})
	s := newServer(t, h, nil)
	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	s.spawnTask(a2a.Task{TaskID: "t-late", Prompt: "p"})
	<-done

	resp, err := http.Get(ts.URL + "/tasks/t-late/events") //nolint:noctx // test
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	updates := readSSE(t, resp)
	require.Len(t, updates, 2)
	assert.Equal(t, a2a.TaskStatusFailed, updates[1].Status)
	assert.Equal(t, "boom", updates[1].Message)
}

func TestHandleEvents_ClientDisconnect(t *testing.T) {
	h, _ := blockingHandler(t)
	s := newServer(t, h, nil)
	state := s.spawnTask(a2a.Task{TaskID: "t-disc", Prompt: "p"})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/tasks/t-disc/events", nil).WithContext(ctx)
	req.SetPathValue("id", "t-disc")

	rec := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		s.handleEvents(rec, req)
	}()

	waitFor(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.subscribers) == 1
	})
	cancel()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvents did not return after client disconnect")
	}
	// The SSE handler must detach its subscriber on the way out.
	state.mu.Lock()
	defer state.mu.Unlock()
	assert.Empty(t, state.subscribers)
}

func TestHandleEvents_ChannelClosedWithoutTerminalFrame(t *testing.T) {
	// A subscriber whose channel is closed (terminal fan-out) but whose
	// terminal frame was dropped because its buffer was full must still
	// see the stream end.
	s := newServer(t, noopHandler(), nil)
	state := &taskState{id: "t-drop", status: a2a.TaskStatusRunning, cancel: func() {}}
	s.mu.Lock()
	s.tasks[state.id] = state
	s.mu.Unlock()

	ch, cancel := state.subscribe()
	defer cancel()
	// Fill the 16-slot buffer so the terminal update is dropped and only
	// the close() reaches the reader.
	for i := 0; i < cap(ch)+1; i++ {
		state.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Progress: float64(i)})
	}
	state.emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks/t-drop/events", nil)
	req.SetPathValue("id", "t-drop")
	// Drain the pre-filled channel via the handler: it replays history
	// first, then reads the (already closed) live channel and returns.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleEvents(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvents did not return when subscriber channel closed")
	}
	assert.Contains(t, rec.Body.String(), "data: ")
}

func TestHandleEvents_WriteErrorDuringHistoryReplay(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	state := &taskState{id: "t-werr", status: a2a.TaskStatusRunning, cancel: func() {}}
	state.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "one"})
	s.mu.Lock()
	s.tasks[state.id] = state
	s.mu.Unlock()

	w := newErrWriter()
	req := httptest.NewRequest(http.MethodGet, "/tasks/t-werr/events", nil)
	req.SetPathValue("id", "t-werr")

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleEvents(w, req)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvents should bail out when the socket write fails")
	}
	assert.Zero(t, w.flushed, "no flush after a failed write")
}

func TestHandleEvents_WriteErrorOnLiveUpdate(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	state := &taskState{id: "t-werr2", status: a2a.TaskStatusRunning, cancel: func() {}}
	s.mu.Lock()
	s.tasks[state.id] = state
	s.mu.Unlock()

	w := newErrWriter()
	req := httptest.NewRequest(http.MethodGet, "/tasks/t-werr2/events", nil)
	req.SetPathValue("id", "t-werr2")

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleEvents(w, req)
	}()
	waitFor(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.subscribers) == 1
	})
	state.emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "live"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvents should bail out when a live write fails")
	}
}

// ---- spawnTask -------------------------------------------------------

func TestSpawnTask(t *testing.T) {
	t.Run("handler error becomes a failed update", func(t *testing.T) {
		s := newServer(t, handlerFunc(func(context.Context, a2a.Task, func(a2a.TaskUpdate)) error {
			return errors.New("kaboom")
		}), nil)
		state := s.spawnTask(a2a.Task{TaskID: "t-err", Prompt: "p"})
		waitFor(t, state.isTerminal)

		snap := state.snapshot()
		assert.Equal(t, a2a.TaskStatusFailed, snap.Status)
		assert.Equal(t, "kaboom", snap.Last.Message)
		assert.Equal(t, "handler_error", snap.Last.FailureCode)
		assert.Equal(t, "t-err", snap.Last.TaskID)
	})

	t.Run("handler error after a terminal emit is not double-reported", func(t *testing.T) {
		s := newServer(t, handlerFunc(func(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
			emit(a2a.TaskUpdate{Status: a2a.TaskStatusCancelled, Message: "aborted"})
			return errors.New("ignored")
		}), nil)
		state := s.spawnTask(a2a.Task{TaskID: "t-term", Prompt: "p"})
		waitFor(t, state.isTerminal)
		// Let any (incorrect) extra emit land before asserting.
		time.Sleep(50 * time.Millisecond)

		snap := state.snapshot()
		assert.Equal(t, a2a.TaskStatusCancelled, snap.Status)
		assert.Equal(t, "aborted", snap.Last.Message)
		assert.Equal(t, 1, snap.NumUpdates)
	})

	t.Run("silent handler gets a synthesised completed update", func(t *testing.T) {
		s := newServer(t, noopHandler(), nil)
		state := s.spawnTask(a2a.Task{TaskID: "t-silent", Prompt: "p"})
		waitFor(t, state.isTerminal)

		snap := state.snapshot()
		assert.Equal(t, a2a.TaskStatusCompleted, snap.Status)
		assert.Equal(t, 1, snap.NumUpdates)
	})

	t.Run("emit preserves caller-supplied TaskID and timestamp", func(t *testing.T) {
		at := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
		s := newServer(t, handlerFunc(func(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
			emit(a2a.TaskUpdate{TaskID: "explicit", Status: a2a.TaskStatusCompleted, At: at})
			return nil
		}), nil)
		state := s.spawnTask(a2a.Task{TaskID: "t-stamp", Prompt: "p"})
		waitFor(t, state.isTerminal)

		snap := state.snapshot()
		assert.Equal(t, "explicit", snap.Last.TaskID)
		assert.True(t, at.Equal(snap.Last.At))
	})

	t.Run("task payload reaches the handler", func(t *testing.T) {
		var got a2a.Task
		s := newServer(t, handlerFunc(func(_ context.Context, task a2a.Task, _ func(a2a.TaskUpdate)) error {
			got = task
			return nil
		}), nil)
		want := a2a.Task{
			TaskID:         "t-payload",
			FromAgent:      "peer",
			SkillName:      "review",
			Prompt:         "look at this",
			InputArtifacts: []a2a.Artifact{{URI: "artifact://1", Name: "diff"}},
		}
		state := s.spawnTask(want)
		waitFor(t, state.isTerminal)
		assert.Equal(t, want, got)
	})
}

func TestSpawnTask_ConcurrentTasksAreIsolated(t *testing.T) {
	s := newServer(t, handlerFunc(func(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: task.Prompt})
		return nil
	}), nil)

	var wg sync.WaitGroup
	states := make([]*taskState, 25)
	for i := range states {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			states[i] = s.spawnTask(a2a.Task{TaskID: fmt.Sprintf("t-%d", i), Prompt: fmt.Sprintf("p-%d", i)})
		}(i)
	}
	wg.Wait()

	for i, st := range states {
		waitFor(t, st.isTerminal)
		assert.Equal(t, fmt.Sprintf("p-%d", i), st.snapshot().Last.OutputText)
	}
	s.mu.Lock()
	assert.Len(t, s.tasks, 25)
	s.mu.Unlock()
}

// ---- helpers under test ---------------------------------------------

func TestWriteSSE(t *testing.T) {
	t.Run("frames are newline delimited", func(t *testing.T) {
		var sb strings.Builder
		require.NoError(t, writeSSE(&sb, a2a.TaskUpdate{TaskID: "a", Status: a2a.TaskStatusRunning}))
		assert.True(t, strings.HasPrefix(sb.String(), "data: {"))
		assert.True(t, strings.HasSuffix(sb.String(), "\n\n"))
	})

	t.Run("marshal failure is surfaced", func(t *testing.T) {
		var sb strings.Builder
		err := writeSSE(&sb, a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Progress: math.NaN()})
		require.Error(t, err)
		assert.Empty(t, sb.String())
	})

	t.Run("write failure is surfaced", func(t *testing.T) {
		err := writeSSE(newErrWriter(), a2a.TaskUpdate{Status: a2a.TaskStatusRunning})
		require.Error(t, err)
	})
}

func TestWriteJSONAndWriteErr(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusTeapot, "nope")
	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":"nope"}`, rec.Body.String())

	// An unencodable body still sets the status line.
	rec2 := httptest.NewRecorder()
	writeJSON(rec2, http.StatusOK, map[string]float64{"p": math.Inf(1)})
	assert.Equal(t, http.StatusOK, rec2.Code)
}

func TestNewTaskID(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := newTaskID()
		assert.True(t, strings.HasPrefix(id, "task-"), id)
		assert.Len(t, id, len("task-")+32)
		_, dup := seen[id]
		assert.False(t, dup, "task ids must be unique")
		seen[id] = struct{}{}
	}
}

func TestNewTaskID_RandFailureFallsBackToTimestamp(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("entropy exhausted") }

	id := newTaskID()
	assert.True(t, strings.HasPrefix(id, "task-"), id)
	assert.NotEqual(t, "task-", id)
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status a2a.TaskStatus
		want   bool
	}{
		{a2a.TaskStatusCompleted, true},
		{a2a.TaskStatusFailed, true},
		{a2a.TaskStatusCancelled, true},
		{a2a.TaskStatusRunning, false},
		{a2a.TaskStatus(""), false},
		{a2a.TaskStatus("weird"), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			assert.Equal(t, tc.want, isTerminal(tc.status))
		})
	}
}

func TestLookup_UnknownReturnsNil(t *testing.T) {
	s := newServer(t, noopHandler(), nil)
	assert.Nil(t, s.lookup("missing"))
}

// ---- shared test utilities -------------------------------------------

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// readSSE drains an SSE response body into decoded TaskUpdates. It
// reads until the server closes the stream.
func readSSE(t *testing.T, resp *http.Response) []a2a.TaskUpdate {
	t.Helper()
	var out []a2a.TaskUpdate
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var upd a2a.TaskUpdate
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &upd))
		out = append(out, upd)
	}
	return out
}
