package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	a2aclient "github.com/sebastienrousseau/rousseau-agent/internal/a2a/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/server"
)

// echoHandler is a trivial a2a.server.Handler that turns the task
// prompt into a completed update. Every test-side peer uses this.
type echoHandler struct{ prefix string }

func (h echoHandler) OnTask(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "thinking"})
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: h.prefix + task.Prompt})
	return nil
}

// startTestPeer stands a real a2a.server on a loopback port. Returns
// a Client wired to it and a cleanup func.
func startTestPeer(t *testing.T, prefix string) *a2aclient.Client {
	t.Helper()
	srv, err := server.New(a2a.CapabilityCard{
		Name: "test-peer", Version: "v0.0.5",
	}, echoHandler{prefix: prefix}, nil)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }() //nolint:errcheck // test cleanup below
	t.Cleanup(func() {
		_ = httpSrv.Close() //nolint:errcheck // test cleanup
	})

	c, err := a2aclient.New(a2aclient.Config{
		Name:     "test-peer",
		Endpoint: "http://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
	})
	require.NoError(t, err)
	return c
}

// --- schema + description shape ---------------------------------

func TestA2ADispatchTool_NameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{
		"peer-a": nil, "peer-b": nil, // real client not needed for schema
	})
	assert.Equal(t, "a2a_dispatch", tool.Name())
	schema := tool.InputSchema()

	required, _ := schema["required"].([]string)
	assert.Equal(t, []string{"peer", "prompt"}, required)

	props := schema["properties"].(map[string]any)
	peer := props["peer"].(map[string]any)
	enum, ok := peer["enum"].([]string)
	require.True(t, ok)
	// Sorted deterministically so twice-called schema is stable.
	assert.Equal(t, []string{"peer-a", "peer-b"}, enum)
}

func TestA2ADispatchTool_DescriptionListsPeers(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{
		"spec-writer": nil, "reviewer": nil,
	})
	got := tool.Description()
	assert.Contains(t, got, "spec-writer")
	assert.Contains(t, got, "reviewer")
	// The tool's terminal behaviour clause must be surfaced so the
	// model knows this is a blocking call, not fire-and-forget.
	assert.Contains(t, got, "Blocks until")
}

func TestA2ADispatchTool_NoPeersDescriptionWarnsModel(t *testing.T) {
	t.Parallel()
	// A tool registered without peers should announce that fact so
	// the model doesn't attempt to use it. Freezes the "please
	// don't call this" contract.
	tool := NewA2ADispatchTool(nil)
	got := tool.Description()
	assert.Contains(t, got, "NO PEERS ARE CONFIGURED")
	// Schema still constructs (must be valid JSON schema even when
	// there's nothing to enumerate).
	schema := tool.InputSchema()
	props := schema["properties"].(map[string]any)
	peer := props["peer"].(map[string]any)
	_, hasEnum := peer["enum"]
	assert.False(t, hasEnum, "empty peer list must NOT emit an empty enum (would reject every value)")
}

// --- Execute happy path -----------------------------------------

func TestA2ADispatchTool_Execute_HappyPath(t *testing.T) {
	t.Parallel()
	client := startTestPeer(t, "echo: ")
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"echo": client})
	got, err := tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"echo","prompt":"hello"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hello", got)
}

func TestA2ADispatchTool_Execute_WithSkillName(t *testing.T) {
	t.Parallel()
	// The test peer's echoHandler ignores the SkillName field
	// (it echoes only the Prompt); we're proving the tool
	// accepts + forwards it — not that the peer honours it.
	client := startTestPeer(t, "> ")
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"peer": client})
	got, err := tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"peer","prompt":"do a thing","skill_name":"my-skill"}`))
	require.NoError(t, err)
	assert.Equal(t, "> do a thing", got)
}

// --- Execute validation errors ----------------------------------

func TestA2ADispatchTool_Execute_MalformedJSONInput(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{not-json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse input")
}

func TestA2ADispatchTool_Execute_MissingPeerRejected(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"x": nil})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"hi"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer is required")
}

func TestA2ADispatchTool_Execute_MissingPromptAndSkillRejected(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"x": nil})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"peer":"x"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt or skill_name")
}

func TestA2ADispatchTool_Execute_UnknownPeerRejected(t *testing.T) {
	t.Parallel()
	client := startTestPeer(t, "")
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"known": client})
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"unknown","prompt":"hi"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown peer")
	// The list of known peers must be in the error for the model
	// to self-correct instead of retrying blind.
	assert.Contains(t, err.Error(), "known")
}

// --- peer failure paths -----------------------------------------

// failHandler emits a failure update — proves the tool surfaces
// peer-side failures cleanly.
type failHandler struct{ code, msg string }

func (h failHandler) OnTask(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{
		Status:      a2a.TaskStatusFailed,
		FailureCode: h.code,
		Message:     h.msg,
	})
	return nil
}

func TestA2ADispatchTool_Execute_PeerFailureSurfaces(t *testing.T) {
	t.Parallel()
	srv, err := server.New(a2a.CapabilityCard{Name: "p", Version: "v"},
		failHandler{code: "policy_deny", msg: "not allowed by peer"}, nil)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()     //nolint:errcheck // test
	t.Cleanup(func() { _ = httpSrv.Close() }) //nolint:errcheck // test

	client, err := a2aclient.New(a2aclient.Config{
		Name: "failer", Endpoint: "http://" + ln.Addr().String(), Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"failer": client})
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"failer","prompt":"hi"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer \"failer\" reported failure")
	assert.Contains(t, err.Error(), "policy_deny")
	assert.Contains(t, err.Error(), "not allowed by peer")
}

// slowHandler blocks so the tool's timeout fires.
type slowHandler struct{ delay time.Duration }

func (h slowHandler) OnTask(ctx context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	select {
	case <-time.After(h.delay):
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "eventually"})
	case <-ctx.Done():
	}
	return nil
}

func TestA2ADispatchTool_Execute_TimeoutHonoured(t *testing.T) {
	t.Parallel()
	srv, err := server.New(a2a.CapabilityCard{Name: "p", Version: "v"},
		slowHandler{delay: 3 * time.Second}, nil)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()     //nolint:errcheck // test
	t.Cleanup(func() { _ = httpSrv.Close() }) //nolint:errcheck // test

	client, err := a2aclient.New(a2aclient.Config{
		Name: "slow", Endpoint: "http://" + ln.Addr().String(), Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"slow": client})
	// 100ms timeout — much less than the handler's 3s delay.
	start := time.Now()
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"slow","prompt":"wait","timeout_seconds":0.1}`))
	elapsed := time.Since(start)
	require.Error(t, err)
	// Wall-clock check: the tool must return within a small
	// multiplier of the timeout, NOT the peer's 3s delay.
	assert.Less(t, elapsed, 1*time.Second, "timeout_seconds was NOT honoured")
	// The specific error message depends on WHERE the timeout hit
	// (initial POST, SSE open, or mid-stream) — any of these is
	// acceptable proof that the timeout fired.
	assert.True(t,
		strings.Contains(err.Error(), "stream ended") ||
			strings.Contains(err.Error(), "deadline exceeded") ||
			strings.Contains(err.Error(), "context canceled"),
		"expected timeout-shaped error, got: %v", err,
	)
}

// dropHandler closes the stream without emitting a terminal update
// (simulates a peer that hung up mid-conversation).
type dropHandler struct{}

func (dropHandler) OnTask(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning})
	return errors.New("simulated peer crash")
}

func TestA2ADispatchTool_Execute_NoTerminalStatusReported(t *testing.T) {
	t.Parallel()
	// dropHandler returns an error which the a2a/server layer will
	// translate into a synthesised Failed update. So the tool
	// actually SEES a terminal Failed — this test proves that path
	// surfaces the WARN reason.
	srv, err := server.New(a2a.CapabilityCard{Name: "p", Version: "v"},
		dropHandler{}, nil)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()     //nolint:errcheck // test
	t.Cleanup(func() { _ = httpSrv.Close() }) //nolint:errcheck // test

	client, err := a2aclient.New(a2aclient.Config{
		Name: "drop", Endpoint: "http://" + ln.Addr().String(), Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"drop": client})
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"drop","prompt":"hi"}`))
	require.Error(t, err)
	// A a2a/server error becomes handler_error failure code.
	assert.True(t,
		strings.Contains(err.Error(), "handler_error") ||
			strings.Contains(err.Error(), "reported failure"),
		"expected peer-failure surface, got: %v", err,
	)
}

// noOutputHandler completes without emitting any OutputText.
type noOutputHandler struct{}

func (noOutputHandler) OnTask(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted})
	return nil
}

func TestA2ADispatchTool_Execute_EmptyOutputSurfacesSentinel(t *testing.T) {
	t.Parallel()
	// Freezes the "no output" contract: return a friendly sentinel
	// string instead of an empty string so the model can reason
	// about the situation.
	srv, err := server.New(a2a.CapabilityCard{Name: "p", Version: "v"}, noOutputHandler{}, nil)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()     //nolint:errcheck // test
	t.Cleanup(func() { _ = httpSrv.Close() }) //nolint:errcheck // test

	client, err := a2aclient.New(a2aclient.Config{
		Name: "silent", Endpoint: "http://" + ln.Addr().String(), Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"silent": client})
	got, err := tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"silent","prompt":"hi"}`))
	require.NoError(t, err)
	assert.Equal(t, "(peer completed with no output)", got)
}

// --- multi-part output aggregation ------------------------------

// multiHandler emits multiple OutputText frames before completing —
// the tool must concatenate them.
type multiHandler struct{}

func (multiHandler) OnTask(_ context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, OutputText: "part one"})
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, OutputText: "part two"})
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "final"})
	return nil
}

func TestA2ADispatchTool_Execute_ConcatenatesMultipleOutputs(t *testing.T) {
	t.Parallel()
	srv, err := server.New(a2a.CapabilityCard{Name: "p", Version: "v"}, multiHandler{}, nil)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpSrv := &http.Server{Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()     //nolint:errcheck // test
	t.Cleanup(func() { _ = httpSrv.Close() }) //nolint:errcheck // test

	client, err := a2aclient.New(a2aclient.Config{
		Name: "multi", Endpoint: "http://" + ln.Addr().String(), Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{"multi": client})
	got, err := tool.Execute(context.Background(),
		json.RawMessage(`{"peer":"multi","prompt":"go"}`))
	require.NoError(t, err)
	assert.Equal(t, "part one\npart two\nfinal", got)
}

// --- peerNames sorting -----------------------------------------

func TestPeerNames_SortedDeterministically(t *testing.T) {
	t.Parallel()
	tool := NewA2ADispatchTool(map[string]*a2aclient.Client{
		"c": nil, "a": nil, "b": nil,
	})
	got := tool.peerNames()
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

func TestPeerNames_EmptyMapIsNil(t *testing.T) {
	t.Parallel()
	assert.Nil(t, NewA2ADispatchTool(nil).peerNames())
	assert.Nil(t, NewA2ADispatchTool(map[string]*a2aclient.Client{}).peerNames())
}
