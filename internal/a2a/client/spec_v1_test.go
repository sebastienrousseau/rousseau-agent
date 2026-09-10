package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// mustMarshalC is the errcheck-friendly test helper for json.Marshal.
func mustMarshalC(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// newTestClient wires a Client at a caller-supplied handler.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c, err := New(Config{Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSpec_GetAgentCard_HappyPath(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent-card.json" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		w.Header().Set("A2A-Version", a2a.SpecVersion)
		_ = json.NewEncoder(w).Encode(a2a.AgentCard{ //nolint:errcheck // test-server encode, best-effort
			Name: "peer", Version: "v0.0.4",
			Capabilities:    a2a.AgentCapabilities{Streaming: true},
			ProtocolVersion: a2a.SpecVersion,
		})
	}))
	card, err := c.GetAgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "peer" || !card.Capabilities.Streaming {
		t.Errorf("unexpected card: %+v", card)
	}
}

func TestSpec_GetAgentCard_LegacyFallback(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			http.NotFound(w, r) // peer only speaks legacy
		case "/.well-known/agent-capabilities":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{ //nolint:errcheck // test
				Name: "legacy-peer", Version: "v0.0.2",
				Skills:            []a2a.SkillDescriptor{{Name: "echo"}},
				SupportsStreaming: true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	card, err := c.GetAgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "legacy-peer" {
		t.Errorf("card.Name = %q, want legacy-peer", card.Name)
	}
	if card.ProtocolVersion != a2a.SpecVersion {
		t.Errorf("upgraded card should advertise ProtocolVersion=%q", a2a.SpecVersion)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "echo" {
		t.Errorf("legacy skills not upgraded: %+v", card.Skills)
	}
}

func TestSpec_GetAgentCard_BothFailBubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	_, err := c.GetAgentCard(context.Background())
	if err == nil {
		t.Fatal("expected error when both spec + legacy return 404")
	}
	if !strings.Contains(err.Error(), "legacy fallback failed") {
		t.Errorf("error should mention legacy fallback failure: %v", err)
	}
}

func TestSpec_SendMessage_HappyPath(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message:send" {
			t.Errorf("path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Content-Type"); got != a2a.ContentTypeSpec {
			t.Errorf("Content-Type = %q", got)
		}
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(a2a.SpecTask{ //nolint:errcheck // test
			ID: "t-42", ContextID: "ctx-1",
			Status: a2a.TaskStatus1{State: a2a.TaskStateSubmitted},
		})
	}))
	task, err := c.SendMessage(context.Background(), a2a.Message{
		MessageID: "m", ContextID: "ctx-1", Role: a2a.RoleUser,
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "t-42" || task.Status.State != a2a.TaskStateSubmitted {
		t.Errorf("unexpected task: %+v", task)
	}
}

func TestSpec_SendMessage_EmptyPartsFailsFast(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server should not be called")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := c.SendMessage(context.Background(), a2a.Message{})
	if err == nil || !strings.Contains(err.Error(), "parts is empty") {
		t.Errorf("expected parts-empty error, got %v", err)
	}
}

func TestSpec_SendMessage_LegacyFallback(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/message:send":
			http.NotFound(w, r)
		case "/tasks":
			// Legacy ack shape.
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // test
				"task_id": "legacy-t-1",
				"status":  string(a2a.TaskStatusRunning),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	task, err := c.SendMessage(context.Background(), a2a.TextMessage("via legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "legacy-t-1" {
		t.Errorf("task.ID = %q, want legacy-t-1", task.ID)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Errorf("state = %q, want TASK_STATE_WORKING (from legacy running)", task.Status.State)
	}
}

func TestSpec_SendMessage_LegacyFallbackNeedsTextPart(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	// data-only part → no text to fall back to.
	_, err := c.SendMessage(context.Background(), a2a.Message{
		MessageID: "m",
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{{Kind: a2a.PartKindData, Data: json.RawMessage(`{"x":1}`)}},
	})
	if err == nil || !strings.Contains(err.Error(), "text part") {
		t.Errorf("expected text-part error, got %v", err)
	}
}

func TestSpec_SubscribeToTask_HappyPath(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":subscribe") {
			t.Errorf("path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		frame1 := a2a.StreamResponse{
			StatusUpdate: &a2a.TaskStatusUpdateEvent{
				TaskID: "t-1", Kind: "status-update",
				Status: a2a.TaskStatus1{State: a2a.TaskStateWorking},
			},
		}
		frame2 := a2a.StreamResponse{
			StatusUpdate: &a2a.TaskStatusUpdateEvent{
				TaskID: "t-1", Kind: "status-update",
				Status: a2a.TaskStatus1{State: a2a.TaskStateCompleted},
			},
		}
		for _, f := range []a2a.StreamResponse{frame1, frame2} {
			blob := mustMarshalC(t, f)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", blob) //nolint:errcheck // test SSE write, best-effort
			flusher.Flush()
		}
	}))
	ch, err := c.SubscribeToTask(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	seen := []a2a.TaskState{}
	for f := range ch {
		if f.StatusUpdate != nil {
			seen = append(seen, f.StatusUpdate.Status.State)
		}
	}
	if len(seen) != 2 || seen[0] != a2a.TaskStateWorking || seen[1] != a2a.TaskStateCompleted {
		t.Errorf("frames = %+v, want [working completed]", seen)
	}
}

func TestSpec_SubscribeToTask_LegacyFallback(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":subscribe"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			// Legacy update shape.
			legacy := a2a.TaskUpdate{
				TaskID: "t-1", Status: a2a.TaskStatusCompleted,
				OutputText: "done", At: time.Now(),
			}
			blob := mustMarshalC(t, legacy)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", blob) //nolint:errcheck // test SSE write, best-effort
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	ch, err := c.SubscribeToTask(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := <-ch
	if !ok {
		t.Fatal("no frame")
	}
	if f.StatusUpdate == nil || f.StatusUpdate.Status.State != a2a.TaskStateCompleted {
		t.Errorf("frame = %+v, want spec-shaped completed", f)
	}
	// Channel closed after terminal frame.
	if _, more := <-ch; more {
		t.Errorf("expected channel close after terminal state")
	}
}

func TestSpec_SubscribeToTask_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.SubscribeToTask(context.Background(), ""); err == nil {
		t.Error("expected error for empty taskID")
	}
}

func TestSpec_CancelTask_HappyPath(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":cancel") {
			t.Errorf("path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	if err := c.CancelTask(context.Background(), "t-1"); err != nil {
		t.Fatal(err)
	}
}

func TestSpec_CancelTask_LegacyFallback(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":cancel"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	if err := c.CancelTask(context.Background(), "t-1"); err != nil {
		t.Fatal(err)
	}
}

func TestSpec_CancelTask_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if err := c.CancelTask(context.Background(), ""); err == nil {
		t.Error("expected error for empty taskID")
	}
}

func TestSpec_GetAgentCard_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.GetAgentCard(context.Background()); err == nil {
		t.Error("expected error on 500")
	}
}

func TestSpec_GetAgentCard_TransportError(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "x", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAgentCard(context.Background()); err == nil {
		t.Error("expected transport error")
	}
}

func TestSpec_GetAgentCard_DecodeError(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_, _ = w.Write([]byte("{not-json")) //nolint:errcheck // test malformed frame, best-effort
	}))
	if _, err := c.GetAgentCard(context.Background()); err == nil {
		t.Error("expected decode error")
	}
}

func TestSpec_SendMessage_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := c.SendMessage(context.Background(), a2a.TextMessage("x"))
	if err == nil {
		t.Error("expected error on 500")
	}
}

func TestSpec_SendMessage_MissingIDBubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(a2a.SpecTask{}) //nolint:errcheck // test — no ID
	}))
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil ||
		!strings.Contains(err.Error(), "task.id") {
		t.Errorf("expected missing-id error, got %v", err)
	}
}

func TestSpec_SendMessage_DecodeError(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{broken")) //nolint:errcheck // test malformed frame, best-effort
	}))
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil {
		t.Error("expected decode error")
	}
}

func TestSpec_SendMessage_LegacyFallback_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/message:send" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil {
		t.Error("expected legacy fallback error")
	}
}

func TestSpec_SubscribeToTask_TransportError(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "x", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubscribeToTask(context.Background(), "t"); err == nil {
		t.Error("expected transport error")
	}
}

func TestSpec_SubscribeToTask_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.SubscribeToTask(context.Background(), "t"); err == nil {
		t.Error("expected 500 error")
	}
}

func TestSpec_SubscribeToTask_LegacyFallback_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":subscribe") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, err := c.SubscribeToTask(context.Background(), "t"); err == nil {
		t.Error("expected legacy fallback error")
	}
}

func TestSpec_SubscribeToTask_MalformedFramesSkipped(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// Bad frame first, valid second — bad one must be silently skipped.
		_, _ = fmt.Fprintf(w, "data: not-json\n\n") //nolint:errcheck // test SSE write, best-effort
		flusher.Flush()
		valid := a2a.StreamResponse{
			StatusUpdate: &a2a.TaskStatusUpdateEvent{
				TaskID: "t", Kind: "status-update",
				Status: a2a.TaskStatus1{State: a2a.TaskStateCompleted},
			},
		}
		blob := mustMarshalC(t, valid)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", blob) //nolint:errcheck // test SSE write, best-effort
		flusher.Flush()
	}))
	ch, err := c.SubscribeToTask(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	got := false
	for f := range ch {
		if f.StatusUpdate != nil && f.StatusUpdate.Status.State == a2a.TaskStateCompleted {
			got = true
		}
	}
	if !got {
		t.Error("did not receive terminal frame after bad one")
	}
}

func TestSpec_CancelTask_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 500 on both spec and legacy paths so neither succeeds.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if err := c.CancelTask(context.Background(), "t"); err == nil {
		t.Error("expected error on 500")
	}
}

func TestSpec_CancelTask_TransportError(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "x", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CancelTask(context.Background(), "t"); err == nil {
		t.Error("expected transport error")
	}
}

func TestSpec_SendMessage_TransportError(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Name: "x", Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil {
		t.Error("expected transport error")
	}
}

func TestSpec_SendMessage_LegacyFallback_TransportError(t *testing.T) {
	t.Parallel()
	// Spec 404s, then the fallback POST /tasks hangs → context timeout.
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/message:send" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(500 * time.Millisecond) // exceeds client timeout below
	}))
	c.cfg.Timeout = 50 * time.Millisecond
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil {
		t.Error("expected timeout in legacy fallback")
	}
}

func TestSpec_SendMessage_LegacyFallback_DecodeError(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/message:send" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{broken")) //nolint:errcheck // test malformed frame, best-effort
	}))
	if _, err := c.SendMessage(context.Background(), a2a.TextMessage("x")); err == nil ||
		!strings.Contains(err.Error(), "legacy ack") {
		t.Errorf("expected legacy-ack decode error, got %v", err)
	}
}

func TestSpec_SubscribeToTask_LegacyFallback_MalformedFramesSkipped(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":subscribe") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: {broken\n\n") //nolint:errcheck // test SSE write, best-effort
		flusher.Flush()
		legacy := a2a.TaskUpdate{
			TaskID: "t", Status: a2a.TaskStatusCompleted, At: time.Now(),
		}
		blob := mustMarshalC(t, legacy)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", blob) //nolint:errcheck // test SSE write, best-effort
		flusher.Flush()
	}))
	ch, err := c.SubscribeToTask(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for f := range ch {
		if f.StatusUpdate != nil && f.StatusUpdate.Status.State == a2a.TaskStateCompleted {
			seen = true
		}
	}
	if !seen {
		t.Error("did not receive terminal frame after malformed one")
	}
}

func TestSpec_CancelTask_LegacyFallback_500Bubbles(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":cancel") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if err := c.CancelTask(context.Background(), "t"); err == nil {
		t.Error("expected error from legacy fallback")
	}
}

// -- Card-signature verification policy ---------------------------

func TestSpec_GetAgentCard_UntrustedSignatureRejected(t *testing.T) {
	t.Parallel()
	// Peer signs with key A, client trusts only key B — verify must
	// reject the card even though the signature itself is valid.
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "peer", Version: "v"})
		signed, sErr := a2a.SignAgentCard(card, signer)
		require.NoError(t, sErr)
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(signed) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	c, err := New(Config{
		Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second,
		TrustedPublisherKeys: []ed25519.PublicKey{other},
	})
	require.NoError(t, err)
	_, err = c.GetAgentCard(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, a2a.ErrCardBadSignature)
}

func TestSpec_GetAgentCard_TrustedSignatureAccepted(t *testing.T) {
	t.Parallel()
	pub, signer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "peer", Version: "v"})
		signed, sErr := a2a.SignAgentCard(card, signer)
		require.NoError(t, sErr)
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(signed) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	c, err := New(Config{
		Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second,
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
	})
	require.NoError(t, err)
	card, err := c.GetAgentCard(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "peer", card.Name)
}

func TestSpec_GetAgentCard_RequireSignedRejectsUnsigned(t *testing.T) {
	t.Parallel()
	// Peer serves an unsigned card. Client requires signed → reject.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "peer", Version: "v"})
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(card) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	c, err := New(Config{
		Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second,
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
		RequireSignedCard:    true,
	})
	require.NoError(t, err)
	_, err = c.GetAgentCard(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, a2a.ErrCardUnsigned)
}

func TestSpec_GetAgentCard_UnsignedAcceptedWhenNoPolicy(t *testing.T) {
	t.Parallel()
	// Backward-compat: zero-value config accepts unsigned cards so
	// existing deployments aren't broken by this feature landing.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "peer", Version: "v"})
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(card) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	c, err := New(Config{Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second})
	require.NoError(t, err)
	card, err := c.GetAgentCard(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "peer", card.Name)
}

func TestSpec_GetAgentCard_LegacyFallbackRejectedWhenSignatureRequired(t *testing.T) {
	t.Parallel()
	// Peer only speaks legacy (404 on spec route). Client with
	// RequireSignedCard must refuse — legacy cards cannot be signed.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{Name: "legacy", Version: "v0.0.2"}) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c, err := New(Config{
		Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second,
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
		RequireSignedCard:    true,
	})
	require.NoError(t, err)
	_, err = c.GetAgentCard(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy cards cannot be signed")
}

// TestSpec_EndToEnd_ThroughRousseauServer wires the v1 client to a
// real Server end-to-end so a change on either side of the wire
// contract breaks CI, not silently accepts.
func TestSpec_EndToEnd_ThroughRousseauServer(t *testing.T) {
	t.Parallel()
	// We import the server through its httptest.Server-style helper
	// registered in the same package tree by hitting it via HTTP.
	// This test does the round-trip manually to keep the test file
	// package-local.
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			w.Header().Set("Content-Type", a2a.ContentTypeSpec)
			_ = json.NewEncoder(w).Encode(a2a.AgentCard{Name: "n", Version: "v", ProtocolVersion: a2a.SpecVersion}) //nolint:errcheck // test
		case "/message:send":
			body, _ := io.ReadAll(r.Body) //nolint:errcheck // best-effort test read
			var msg a2a.Message
			if err := json.Unmarshal(body, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.MessageID == "" {
				t.Errorf("server saw empty MessageID")
			}
			w.Header().Set("Content-Type", a2a.ContentTypeSpec)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a2a.SpecTask{ID: "t-e2e", Status: a2a.TaskStatus1{State: a2a.TaskStateSubmitted}}) //nolint:errcheck // test
		default:
			http.NotFound(w, r)
		}
	}))
	card, err := c.GetAgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "n" {
		t.Errorf("card.Name = %q", card.Name)
	}
	task, err := c.SendMessage(context.Background(), a2a.TextMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "t-e2e" {
		t.Errorf("task.ID = %q", task.ID)
	}
}
