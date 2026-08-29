package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

func TestBashTool_RunsEcho(t *testing.T) {
	tool := NewBashTool(2 * time.Second)
	in := json.RawMessage(`{"command": "printf hello"}`)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool(50 * time.Millisecond)
	in := json.RawMessage(`{"command": "sleep 2"}`)
	_, err := tool.Execute(context.Background(), in)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "signal:"))
}

func TestBashTool_MissingCommand(t *testing.T) {
	tool := NewBashTool(0)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Error(t, err)
}

func TestBashTool_Metadata(t *testing.T) {
	tool := NewBashTool(0)
	assert.Equal(t, "bash", tool.Name())
	assert.NotEmpty(t, tool.Description())
	schema := tool.InputSchema()
	assert.Equal(t, "object", schema["type"])
}

// fakeBackend is a sandbox.Backend for tests. Records every command
// so the sandboxed path can be asserted without a real gvisor/nsjail
// binary. Optional runErr / runOut let one test cover the error path.
type fakeBackend struct {
	mu   sync.Mutex
	seen []sandbox.Command
	out  string
	err  error
}

func (f *fakeBackend) Kind() string { return "fake" }
func (f *fakeBackend) Run(_ context.Context, cmd sandbox.Command) (sandbox.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, cmd)
	return sandbox.Result{CombinedOutput: f.out}, f.err
}

func TestBashTool_RoutesThroughSandboxWhenSet(t *testing.T) {
	// The point of NewBashToolWithSandbox: the direct-exec path is
	// not taken. Proof: a fakeBackend that returns fixed output —
	// regardless of what the command actually says — shows the tool
	// obeyed the backend's Result.
	fb := &fakeBackend{out: "sandbox-said-so"}
	tool := NewBashToolWithSandbox(2*time.Second, fb)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf ignored"}`))
	require.NoError(t, err)
	assert.Equal(t, "sandbox-said-so", out)
	require.Len(t, fb.seen, 1)
	assert.Equal(t, "/bin/sh", fb.seen[0].Path)
	assert.Equal(t, []string{"-c", "printf ignored"}, fb.seen[0].Args)
}

func TestBashTool_SandboxErrorSurfaces(t *testing.T) {
	fb := &fakeBackend{out: "partial stderr", err: errors.New("runsc: exit 137")}
	tool := NewBashToolWithSandbox(2*time.Second, fb)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"true"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runsc: exit 137")
	assert.Equal(t, "partial stderr", out, "the caller wants the partial output on failure too")
}

func TestBashTool_NewWithSandboxNilBackendUsesDirect(t *testing.T) {
	// nil backend must degrade to the direct-exec path — matches
	// NewBashTool's constructor contract.
	tool := NewBashToolWithSandbox(2*time.Second, nil)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf direct"}`))
	require.NoError(t, err)
	assert.Equal(t, "direct", out)
}
