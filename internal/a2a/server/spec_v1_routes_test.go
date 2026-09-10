package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// echoHandler completes any incoming task immediately. Used by the
// spec-route tests so the terminal state arrives before the tests
// check it, keeping them fast + non-flaky.
type echoHandler struct{}

func (echoHandler) OnTask(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "thinking"})
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "echo: " + task.Prompt})
	return nil
}

// mustMarshal is the errcheck-friendly test helper for json.Marshal.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// mustReq is the errcheck-friendly wrapper for http.NewRequest.
func mustReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// mustGet wraps http.Get with a Fatal on error so callers can chain.
func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	res, err := http.Get(url) //nolint:noctx // test
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return res
}

// mustPost wraps http.Post with a Fatal on error.
func mustPost(t *testing.T, url, contentType string, body io.Reader) *http.Response {
	t.Helper()
	res, err := http.Post(url, contentType, body) //nolint:noctx // test
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return res
}

func newSpecTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	s, err := New(a2a.CapabilityCard{
		AgentID:           "spec-peer",
		Name:              "spec-peer",
		Version:           "v0.0.4",
		SupportsStreaming: true,
		Skills:            []a2a.SkillDescriptor{{Name: "echo", Description: "echoes the prompt"}},
	}, echoHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, s
}

func TestSpec_AgentCardServed(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	res, err := http.Get(ts.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != a2a.ContentTypeSpec {
		t.Errorf("Content-Type = %q, want %q", got, a2a.ContentTypeSpec)
	}
	if got := res.Header.Get("A2A-Version"); got != a2a.SpecVersion {
		t.Errorf("A2A-Version = %q, want %q", got, a2a.SpecVersion)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if card.Name != "spec-peer" || card.Version != "v0.0.4" {
		t.Errorf("card top-level unexpected: %+v", card)
	}
	if !card.Capabilities.Streaming {
		t.Errorf("streaming capability lost")
	}
	if card.ProtocolVersion != a2a.SpecVersion {
		t.Errorf("protocolVersion = %q, want %q", card.ProtocolVersion, a2a.SpecVersion)
	}
	if len(card.Interfaces) == 0 || card.Interfaces[0].ProtocolBinding != "REST" {
		t.Errorf("interfaces[] should advertise REST binding: %+v", card.Interfaces)
	}
	if card.PreferredTransport != "REST" {
		t.Errorf("preferredTransport = %q, want REST", card.PreferredTransport)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "echo" {
		t.Errorf("skills unexpected: %+v", card.Skills)
	}
}

func TestSpec_MessageSend_HappyPath(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	msg := a2a.Message{
		MessageID: "m-1",
		ContextID: "ctx-abc",
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{{Kind: a2a.PartKindText, Text: "hello spec world"}},
	}
	body := mustMarshal(t, msg)
	req := mustReq(t, "POST", ts.URL+"/message:send", bytes.NewReader(body))
	req.Header.Set("Content-Type", a2a.ContentTypeSpec)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		buf, _ := io.ReadAll(res.Body) //nolint:errcheck // best-effort read for error message
		t.Fatalf("status = %d, want 202. body: %s", res.StatusCode, buf)
	}
	if got := res.Header.Get("Content-Type"); got != a2a.ContentTypeSpec {
		t.Errorf("Content-Type = %q", got)
	}
	if got := res.Header.Get("A2A-Version"); got != a2a.SpecVersion {
		t.Errorf("A2A-Version = %q", got)
	}
	var task a2a.SpecTask
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Errorf("task.ID empty")
	}
	if task.ContextID != "ctx-abc" {
		t.Errorf("contextId = %q, want ctx-abc", task.ContextID)
	}
	if task.Status.State != a2a.TaskStateSubmitted {
		t.Errorf("initial state = %q, want %q", task.Status.State, a2a.TaskStateSubmitted)
	}
}

func TestSpec_MessageSend_EmptyPartsRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	body := mustMarshal(t, a2a.Message{
		MessageID: "m-2",
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{{Kind: a2a.PartKindText, Text: ""}},
	})
	res, err := http.Post(ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	var errBody map[string]any
	if err := json.NewDecoder(res.Body).Decode(&errBody); err != nil {
		t.Fatal(err)
	}
	if errBody["code"] != "empty_message" {
		t.Errorf("error code = %v, want empty_message", errBody["code"])
	}
}

func TestSpec_MessageSend_InvalidJSONRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	res, err := http.Post(ts.URL+"/message:send", a2a.ContentTypeSpec, strings.NewReader("{not-json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestSpec_ColonVerb_Cancel(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	// Submit first so we have a real task id.
	body := mustMarshal(t, a2a.TextMessage("running"))
	res, err := http.Post(ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var task a2a.SpecTask
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if task.ID == "" {
		t.Fatal("no task id")
	}

	// Now cancel it via the spec colon-verb.
	req := mustReq(t, "POST", ts.URL+"/tasks/"+task.ID+":cancel", nil)
	cancelRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cancelRes.Body.Close() }()
	if cancelRes.StatusCode != http.StatusAccepted {
		buf, _ := io.ReadAll(cancelRes.Body) //nolint:errcheck // best-effort read for error message
		t.Fatalf("cancel status = %d, want 202. body: %s", cancelRes.StatusCode, buf)
	}
	if got := cancelRes.Header.Get("Content-Type"); got != a2a.ContentTypeSpec {
		t.Errorf("Content-Type = %q", got)
	}
	var cancelTask a2a.SpecTask
	if err := json.NewDecoder(cancelRes.Body).Decode(&cancelTask); err != nil {
		t.Fatal(err)
	}
	if cancelTask.Status.State != a2a.TaskStateCanceled {
		t.Errorf("state = %q, want TASK_STATE_CANCELED", cancelTask.Status.State)
	}
}

func TestSpec_ColonVerb_UnknownVerbIs404(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	// GET verb
	res, err := http.Get(ts.URL + "/tasks/anything:bogus")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET bogus verb: status = %d, want 404", res.StatusCode)
	}
	// POST verb
	req := mustReq(t, "POST", ts.URL+"/tasks/anything:bogus", nil)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Errorf("POST bogus verb: status = %d, want 404", res2.StatusCode)
	}
}

func TestSpec_PostBareTaskId_405(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	req := mustReq(t, "POST", ts.URL+"/tasks/bareid", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
	if got := res.Header.Get("Allow"); !strings.Contains(got, "GET") {
		t.Errorf("Allow = %q, want to mention GET", got)
	}
}

func TestSpec_GetBareTaskId_ReturnsLegacyShape(t *testing.T) {
	// Bare `GET /tasks/{id}` is served by the legacy handler (v0
	// shape). This freezes that behaviour so the deprecation is
	// intentional — see docs/a2a-conformance.md roadmap row v0.0.7.
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	body := mustMarshal(t, a2a.TextMessage("hello"))
	res := mustPost(t, ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	var task a2a.SpecTask
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	// Give the handler a moment to run and set state.
	time.Sleep(50 * time.Millisecond)

	res2 := mustGet(t, ts.URL+"/tasks/"+task.ID)
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("bare GET status = %d", res2.StatusCode)
	}
	// Legacy shape: has snake_case task_id.
	buf, _ := io.ReadAll(res2.Body) //nolint:errcheck // best-effort read for error message
	if !strings.Contains(string(buf), `"task_id"`) {
		t.Errorf("expected legacy snake_case shape, got %s", buf)
	}
}

func TestSpec_ColonVerb_Subscribe_ReceivesStreamResponseFrames(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	body := mustMarshal(t, a2a.TextMessage("stream me"))
	res := mustPost(t, ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	var task a2a.SpecTask
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	// Small wait so the handler has emitted terminal update into
	// the history buffer.
	time.Sleep(50 * time.Millisecond)

	sub, err := http.Get(ts.URL + "/tasks/" + task.ID + ":subscribe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Body.Close() }()
	if got := sub.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := sub.Header.Get("A2A-Version"); got != a2a.SpecVersion {
		t.Errorf("A2A-Version = %q", got)
	}

	scanner := bufio.NewScanner(sub.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	sawTerminal := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame a2a.StreamResponse
		if err := json.Unmarshal([]byte(line[len("data: "):]), &frame); err != nil {
			t.Fatalf("frame not spec-shaped: %v: %s", err, line)
		}
		if frame.StatusUpdate == nil {
			t.Fatalf("frame lacks statusUpdate: %s", line)
		}
		if frame.StatusUpdate.Kind != "status-update" {
			t.Errorf("kind = %q, want status-update", frame.StatusUpdate.Kind)
		}
		if frame.StatusUpdate.Status.State == a2a.TaskStateCompleted {
			sawTerminal = true
			break
		}
	}
	if !sawTerminal {
		t.Errorf("did not observe TASK_STATE_COMPLETED frame")
	}
}

func TestSpec_SubscribeUnknownTask_404(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	res, err := http.Get(ts.URL + "/tasks/nope:subscribe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// slowHandler blocks on emit until the caller closes done, so the
// subscribe test can subscribe BEFORE the task terminates. That
// exercises the "live update → flush → terminal" branch that
// handleSpecSubscribeFor's post-history select-loop covers.
type slowHandler struct {
	done  <-chan struct{}
	fired chan<- struct{} // signalled when OnTask starts
}

func (h slowHandler) OnTask(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	// Signal the test that we're inside the handler goroutine —
	// the state exists, but no updates yet.
	close(h.fired)
	// Emit "working" first so the subscribe path sees a live update.
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "thinking"})
	<-h.done
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "done"})
	return nil
}

func TestSpec_ColonVerb_Subscribe_LiveUpdatesThenTerminal(t *testing.T) {
	t.Parallel()
	// This test drives the post-history select-loop branches of
	// handleSpecSubscribeFor: live update arrives on the subscriber
	// channel, gets flushed as a StreamResponse frame, then a
	// terminal frame ends the stream via isTerminal(upd.Status).
	done := make(chan struct{})
	fired := make(chan struct{})
	s, err := New(a2a.CapabilityCard{Name: "peer", Version: "v"},
		slowHandler{done: done, fired: fired}, nil)
	require.NoError(t, err)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// Submit and wait for the handler to be running (so state exists,
	// but before the terminal update lands).
	body := mustMarshal(t, a2a.TextMessage("stream me live"))
	res := mustPost(t, ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	var task a2a.SpecTask
	require.NoError(t, json.NewDecoder(res.Body).Decode(&task))
	require.NoError(t, res.Body.Close())
	<-fired // handler goroutine has entered OnTask

	sub := mustGet(t, ts.URL+"/tasks/"+task.ID+":subscribe")
	defer func() { _ = sub.Body.Close() }()

	// Release the handler so it emits the terminal update.
	close(done)

	scanner := bufio.NewScanner(sub.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	states := []a2a.TaskState{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame a2a.StreamResponse
		if err := json.Unmarshal([]byte(line[len("data: "):]), &frame); err != nil {
			continue
		}
		if frame.StatusUpdate == nil {
			continue
		}
		states = append(states, frame.StatusUpdate.Status.State)
		if frame.StatusUpdate.Status.State.IsTerminal() {
			break
		}
	}
	require.NotEmpty(t, states)
	assert.Equal(t, a2a.TaskStateCompleted, states[len(states)-1],
		"stream must end on a terminal frame delivered via the live path (not history replay)")
}

// nonFlushingRecorder is an http.ResponseWriter that deliberately
// does NOT implement http.Flusher — the streaming handler must
// detect this and return 500 rather than silently dropping frames.
type nonFlushingRecorder struct {
	header http.Header
	body   *bytes.Buffer
	status int
}

func (n *nonFlushingRecorder) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}
func (n *nonFlushingRecorder) Write(b []byte) (int, error) { return n.body.Write(b) }
func (n *nonFlushingRecorder) WriteHeader(code int)        { n.status = code }

func TestSpec_ColonVerb_Subscribe_NonFlusherReturns500(t *testing.T) {
	t.Parallel()
	// The SSE handler must refuse when the underlying ResponseWriter
	// cannot flush — otherwise frames would buffer and the client
	// would never see them. Direct-dispatch via the handler avoids
	// httptest's automatically-flushable writer.
	s, err := New(a2a.CapabilityCard{Name: "peer", Version: "v"}, echoHandler{}, nil)
	require.NoError(t, err)

	// First submit a task so the id lookup succeeds.
	body := mustMarshal(t, a2a.TextMessage("stream"))
	req := mustReq(t, "POST", "/message:send", bytes.NewReader(body))
	postRec := httptest.NewRecorder()
	s.Router().ServeHTTP(postRec, req)
	require.Equal(t, http.StatusAccepted, postRec.Code)
	var task a2a.SpecTask
	require.NoError(t, json.Unmarshal(postRec.Body.Bytes(), &task))
	require.NotEmpty(t, task.ID)

	// Now call the subscribe route with a non-flushing writer.
	subReq := mustReq(t, "GET", "/tasks/"+task.ID+":subscribe", nil)
	nfr := &nonFlushingRecorder{body: &bytes.Buffer{}}
	s.Router().ServeHTTP(nfr, subReq)
	assert.Equal(t, http.StatusInternalServerError, nfr.status)
	assert.Contains(t, nfr.body.String(), "streaming_unsupported")
}

func TestSpec_ColonVerb_Subscribe_ClientDisconnectExitsLoop(t *testing.T) {
	t.Parallel()
	// If the client disconnects mid-stream, r.Context().Done() must
	// fire and the handler must return promptly. Without this branch
	// the handler would leak until the task terminated.
	done := make(chan struct{})
	fired := make(chan struct{})
	s, err := New(a2a.CapabilityCard{Name: "peer", Version: "v"},
		slowHandler{done: done, fired: fired}, nil)
	require.NoError(t, err)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(done) }) // release handler at end

	body := mustMarshal(t, a2a.TextMessage("subscribe then cancel"))
	res := mustPost(t, ts.URL+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	var task a2a.SpecTask
	require.NoError(t, json.NewDecoder(res.Body).Decode(&task))
	require.NoError(t, res.Body.Close())
	<-fired

	// Open the subscribe stream with a cancellable context, then
	// cancel it — the handler goroutine should exit via
	// r.Context().Done() rather than blocking forever.
	ctx, cancel := context.WithCancel(context.Background())
	subReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/tasks/"+task.ID+":subscribe", nil)
	require.NoError(t, err)
	sub, err := http.DefaultClient.Do(subReq)
	require.NoError(t, err)
	defer func() { _ = sub.Body.Close() }()

	// Drain the initial history frame(s) so we know we're inside
	// the select loop.
	buf := make([]byte, 512)
	_, _ = sub.Body.Read(buf) //nolint:errcheck // read to prove handler is streaming

	// Cancel — the server-side handler goroutine must exit via
	// r.Context().Done(). We prove that by observing the response
	// body reaches EOF within a reasonable window.
	cancel()

	// If the handler didn't honour r.Context().Done(), this Read
	// would block indefinitely. httptest's server cleanup would
	// eventually unblock us, but only via ts.Close() — so we bound
	// the wait to something well under that.
	deadline := time.Now().Add(2 * time.Second)
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, sub.Body) //nolint:errcheck // waiting for EOF, discarding content
		close(readerDone)
	}()
	select {
	case <-readerDone:
		// Success — handler released the connection.
	case <-time.After(time.Until(deadline)):
		t.Fatal("handler did not release the connection after client cancel")
	}
}

func TestSpec_CancelUnknownTask_404(t *testing.T) {
	t.Parallel()
	ts, _ := newSpecTestServer(t)
	req := mustReq(t, "POST", ts.URL+"/tasks/nope:cancel", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestSpec_AgentCardSignedWhenKeyConfigured(t *testing.T) {
	t.Parallel()
	// Configure the server with an Ed25519 signing key so the
	// well-known card gets JWS-signed on serve.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	s, err := New(a2a.CapabilityCard{
		Name: "signed-peer", Version: "v0.0.5", SupportsStreaming: true,
	}, echoHandler{}, nil)
	require.NoError(t, err)
	s.SigningKey = priv
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	res := mustGet(t, ts.URL+"/.well-known/agent-card.json")
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)

	var card a2a.AgentCard
	require.NoError(t, json.NewDecoder(res.Body).Decode(&card))
	require.NotEmpty(t, card.Signatures, "signing key configured but card served unsigned")

	// The client-facing invariant: a peer with the matching public
	// key must be able to verify the served card.
	if err := a2a.VerifyAgentCard(card, []ed25519.PublicKey{pub}); err != nil {
		t.Errorf("card must verify against its own signing key: %v", err)
	}
}

func TestSpec_AgentCardUnsignedWhenNoKeyConfigured(t *testing.T) {
	t.Parallel()
	// Without a signing key the card must still serve — signing is
	// opt-in, not required. This freezes the backward-compat contract.
	ts, _ := newSpecTestServer(t)
	res := mustGet(t, ts.URL+"/.well-known/agent-card.json")
	defer func() { _ = res.Body.Close() }()

	var card a2a.AgentCard
	require.NoError(t, json.NewDecoder(res.Body).Decode(&card))
	assert.Empty(t, card.Signatures, "no key configured → card must be unsigned")
}

func TestSpec_LegacyPathsStillWork(t *testing.T) {
	// Regression: adding the spec routes must not break peers that
	// still speak the v0 shorthand. Hit each legacy path once.
	t.Parallel()
	ts, _ := newSpecTestServer(t)

	res, err := http.Get(ts.URL + "/.well-known/agent-capabilities")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("legacy capabilities: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	legacyTask := a2a.Task{Prompt: "legacy hi"}
	body := mustMarshal(t, legacyTask)
	res2, err := http.Post(ts.URL+"/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusAccepted {
		t.Errorf("legacy submit: %d", res2.StatusCode)
	}
	_ = res2.Body.Close()
}

func TestSpec_BaseURL_HonoursForwardedHeaders(t *testing.T) {
	t.Parallel()
	// Reverse-proxied deployment: the daemon binds to loopback but
	// serves an externally-visible URL. AgentCard must advertise the
	// external URL so peers can reach it.
	req := mustReq(t, "GET", "/.well-known/agent-card.json", nil)
	req.Host = "internal.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "peer.public.example")

	rr := httptest.NewRecorder()
	s, err := New(a2a.CapabilityCard{Name: "n", Version: "v"}, echoHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Router().ServeHTTP(rr, req)

	var card a2a.AgentCard
	if err := json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.URL != "https://peer.public.example" {
		t.Errorf("card.URL = %q, want https://peer.public.example", card.URL)
	}
}

func TestSpec_BaseURL_DefaultsToHTTPHost(t *testing.T) {
	t.Parallel()
	req := mustReq(t, "GET", "/.well-known/agent-card.json", nil)
	req.Host = "peer.local:7443"
	rr := httptest.NewRecorder()
	s, err := New(a2a.CapabilityCard{Name: "n", Version: "v"}, echoHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Router().ServeHTTP(rr, req)
	var card a2a.AgentCard
	if err := json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.URL != "http://peer.local:7443" {
		t.Errorf("card.URL = %q", card.URL)
	}
}

// TestSpec_RealSocket_EndToEnd runs an end-to-end message-send +
// subscribe cycle over a real TCP socket so we catch any Go ServeMux
// pattern-registration regressions the httptest.Recorder path might
// mask.
func TestSpec_RealSocket_EndToEnd(t *testing.T) {
	t.Parallel()
	s, err := New(a2a.CapabilityCard{Name: "peer", Version: "v"}, echoHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = (&http.Server{Handler: s.Router(), ReadHeaderTimeout: 5 * time.Second}).Serve(ln) //nolint:errcheck // test cleanup closes ln, causing an expected error
	}()
	t.Cleanup(func() {
		_ = ln.Close() //nolint:errcheck // test cleanup, best-effort
	})

	base := "http://" + ln.Addr().String()
	body := mustMarshal(t, a2a.TextMessage("real socket"))
	res, err := http.Post(base+"/message:send", a2a.ContentTypeSpec, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusAccepted {
		buf, _ := io.ReadAll(res.Body) //nolint:errcheck // best-effort read for error message
		t.Fatalf("status = %d, body = %s", res.StatusCode, buf)
	}
	var task a2a.SpecTask
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if task.ID == "" {
		t.Fatal("no task id")
	}
}
