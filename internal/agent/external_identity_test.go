package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// L-23: tool calls claude runs itself reach DecideExternal through the
// toolgate socket on a context that carries no identity. The turn's
// verified identity must still govern them (RBAC / OPA / approvals).

// hookingProvider runs during() inside its Complete call: the moment
// a claude subprocess would fire its PreToolUse hook.
type hookingProvider struct{ during func() }

func (p *hookingProvider) Name() string { return "hooking" }
func (p *hookingProvider) Complete(context.Context, Request) (Response, error) {
	p.during()
	return Response{
		Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "done"}}},
		StopReason: StopEndTurn,
	}, nil
}

func identityCapturingApprover(seen *[]string) Approver {
	return ApproverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, string) {
		id, _ := sso.IdentityFromContext(ctx)
		*seen = append(*seen, id.Subject)
		return DecisionAllow, ""
	})
}

func TestDecideExternal_CarriesTheTurnIdentity(t *testing.T) {
	var seen []string
	prov := &hookingProvider{}
	ag := New(prov, tools.NewRegistry(), silentLogger(), Options{Approver: identityCapturingApprover(&seen)})
	s := NewSession("x")
	s.Append(NewUserText("hello"))
	ext := ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`), SessionID: s.ID}
	prov.during = func() {
		// The toolgate server's context: no identity on it.
		ag.DecideExternal(context.Background(), ext)
	}

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "alice"})
	_, err := ag.Turn(ctx, s)
	require.NoError(t, err)

	// After the turn the session's identity is no longer lent out.
	ag.DecideExternal(context.Background(), ext)

	require.Len(t, seen, 2)
	assert.Equal(t, "alice", seen[0], "external tool calls during a turn act as the turn's user")
	assert.Empty(t, seen[1], "an external call outside any turn carries no identity")
}

func TestDecideExternal_OtherSessionsDoNotBorrowIdentity(t *testing.T) {
	var seen []string
	prov := &hookingProvider{}
	ag := New(prov, tools.NewRegistry(), silentLogger(), Options{Approver: identityCapturingApprover(&seen)})
	s := NewSession("x")
	s.Append(NewUserText("hello"))
	prov.during = func() {
		ag.DecideExternal(context.Background(), ApprovalRequest{ToolName: "Bash", SessionID: "someone-else"})
		ag.DecideExternal(context.Background(), ApprovalRequest{ToolName: "Bash", SessionID: ""})
	}
	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "alice"})
	_, err := ag.Turn(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"", ""}, seen)
}
