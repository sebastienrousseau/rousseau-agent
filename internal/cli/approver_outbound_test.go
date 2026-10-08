package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// outboundStub is a tool that declares itself outbound, like gmail_send.
type outboundStub struct{ name string }

func (s outboundStub) Name() string                                           { return s.name }
func (outboundStub) Description() string                                      { return "stub" }
func (outboundStub) InputSchema() map[string]any                              { return map[string]any{"type": "object"} }
func (outboundStub) Outbound() bool                                           { return true }
func (outboundStub) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

// localStub is a tool without the Outbound marker, like read.
type localStub struct{ name string }

func (s localStub) Name() string                                           { return s.name }
func (localStub) Description() string                                      { return "stub" }
func (localStub) InputSchema() map[string]any                              { return map[string]any{"type": "object"} }
func (localStub) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

func outboundTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(outboundStub{name: "gmail_send"}))
	require.NoError(t, reg.Register(localStub{name: "read"}))
	return reg
}

func daemonApprover(t *testing.T, cfg config.ApproverConfig) agent.Approver {
	t.Helper()
	inner, err := buildApprover(cfg)
	require.NoError(t, err)
	return wrapWithOutboundGate(inner, cfg, outboundTestRegistry(t))
}

func TestDefaultApprover_DeniesOutboundTool(t *testing.T) {
	for _, mode := range []string{"", "allow_all", "allow"} {
		t.Run("mode="+mode, func(t *testing.T) {
			ap := daemonApprover(t, config.ApproverConfig{Mode: mode})
			dec, reason := ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "gmail_send"})
			assert.Equal(t, agent.DecisionDeny, dec)
			assert.Equal(t, "outbound tool gmail_send needs an approver rule: set agent.approver.allow_outbound: true or use pattern mode with an allow rule", reason)

			dec, _ = ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
			assert.Equal(t, agent.DecisionAllow, dec)
			dec, _ = ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "not_registered"})
			assert.Equal(t, agent.DecisionAllow, dec, "unknown tools fall through to the agent's own not-found handling")
		})
	}
}

func TestDefaultApprover_AllowOutboundOptIn(t *testing.T) {
	ap := daemonApprover(t, config.ApproverConfig{AllowOutbound: true})
	dec, _ := ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "gmail_send"})
	assert.Equal(t, agent.DecisionAllow, dec)
}

func TestPatternMode_Unchanged(t *testing.T) {
	cfg := config.ApproverConfig{
		Mode:    "pattern",
		Default: "deny",
		Allow:   []config.PatternEntry{{Tool: "gmail_send"}},
	}
	inner, err := buildApprover(cfg)
	require.NoError(t, err)
	ap := wrapWithOutboundGate(inner, cfg, outboundTestRegistry(t))
	assert.Same(t, inner, ap, "pattern mode is not wrapped")
	dec, _ := ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "gmail_send"})
	assert.Equal(t, agent.DecisionAllow, dec)
	dec, _ = ap.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionDeny, dec)
}

func TestOutboundGate_DenyAllUnchanged(t *testing.T) {
	cfg := config.ApproverConfig{Mode: "deny_all"}
	inner, err := buildApprover(cfg)
	require.NoError(t, err)
	assert.Equal(t, inner, wrapWithOutboundGate(inner, cfg, outboundTestRegistry(t)))
}

func TestOutboundGate_NilRegistryPassesThrough(t *testing.T) {
	inner := agent.AllowAllApprover{}
	assert.Equal(t, agent.Approver(inner), wrapWithOutboundGate(inner, config.ApproverConfig{}, nil))
}

func TestCheckOutboundPosture(t *testing.T) {
	cases := []struct {
		name   string
		cfg    config.ApproverConfig
		status string
	}{
		{"default", config.ApproverConfig{}, "info"},
		{"allow_all opted in", config.ApproverConfig{Mode: "allow_all", AllowOutbound: true}, "warn"},
		{"pattern", config.ApproverConfig{Mode: "pattern", AllowOutbound: true}, "info"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Agent.Approver = c.cfg
			rows := checkOutboundPosture(cfg)
			require.Len(t, rows, 1)
			assert.Equal(t, "agent.approver.outbound", rows[0].Name)
			assert.Equal(t, c.status, rows[0].Status)
			assert.NotEmpty(t, rows[0].Detail)
		})
	}
}

func TestRunChecks_IncludesOutboundPosture(t *testing.T) {
	got := runChecks(context.Background(), &config.Config{}, nil)
	found := false
	for _, r := range got {
		found = found || r.Name == "agent.approver.outbound"
	}
	assert.True(t, found)
}

func TestEvidenceControls_OutboundRequiresApproval(t *testing.T) {
	cfg := &config.Config{}
	assert.True(t, evidenceControlsOf(cfg).OutboundRequiresApproval)
	cfg.Agent.Approver.AllowOutbound = true
	assert.False(t, evidenceControlsOf(cfg).OutboundRequiresApproval)
	cfg.Agent.Approver.Mode = "pattern"
	assert.True(t, evidenceControlsOf(cfg).OutboundRequiresApproval)
}
