package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestDecideExternal_HooksAndEmptyReasons(t *testing.T) {
	ctx := context.Background()
	call := ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`), SessionID: "s"}
	mk := func(ap Approver, hk hooks.Runner) *Agent {
		return New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{Approver: ap, Hooks: hk})
	}
	denyQuiet := ApproverFunc(func(context.Context, ApprovalRequest) (Decision, string) { return DecisionDeny, "" })

	d, reason := mk(denyQuiet, nil).DecideExternal(ctx, call)
	assert.Equal(t, DecisionDeny, d)
	assert.Equal(t, "denied by policy", reason)

	cases := []struct {
		name    string
		hook    *stubHooks
		want    Decision
		wantMsg string
	}{
		{"deny with reason", &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionDeny, Reason: "no shell"}}, DecisionDeny, "no shell"},
		{"deny without reason", &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionDeny}}, DecisionDeny, "denied by hook"},
		{"modify cannot apply", &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionModify}}, DecisionDeny, "cannot be applied"},
		{"hook error falls through to its verdict", &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionAllow}, err: errors.New("boom")}, DecisionAllow, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, reason := mk(AllowAllApprover{}, tc.hook).DecideExternal(ctx, call)
			assert.Equal(t, tc.want, d)
			assert.Contains(t, reason, tc.wantMsg)
			require.Len(t, tc.hook.seen, 1, "the hook sees the call")
		})
	}
}

type failingStructured struct {
	stubCompProvider
	err error
}

func (f *failingStructured) CompleteStructured(context.Context, Request, map[string]any) (json.RawMessage, error) {
	return nil, f.err
}

func TestDecide_Errors(t *testing.T) {
	ctx := context.Background()
	_, err := Decide(ctx, &capturingStructured{}, DecisionRequest{Kind: DecisionYesNo, Question: "  "})
	assert.ErrorContains(t, err, "empty question")

	_, err = Decide(ctx, &failingStructured{err: errors.New("cli exited")}, DecisionRequest{Kind: DecisionYesNo, Question: "ok?"})
	assert.ErrorContains(t, err, "cli exited")
}

func TestValidateAgainstSchema(t *testing.T) {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	cases := []struct {
		name   string
		raw    string
		schema map[string]any
		err    string
	}{
		{"not json", `{`, obj(nil), "not JSON"},
		{"not an object", `[]`, obj(nil), "want object"},
		{"missing required ([]string)", `{}`, obj(nil, "answer"), `missing required property "answer"`},
		{"missing required ([]any)", `{}`, map[string]any{"type": "object", "required": []any{"answer"}}, `missing required property "answer"`},
		{"non-schema property ignored", `{"x":1}`, obj(map[string]any{"x": "not a schema"}), ""},
		{"string", `{"s":1}`, obj(map[string]any{"s": map[string]any{"type": "string"}}), "$.s: want string"},
		{"number", `{"n":"1"}`, obj(map[string]any{"n": map[string]any{"type": "number"}}), "want number"},
		{"integer rejects fraction", `{"i":1.5}`, obj(map[string]any{"i": map[string]any{"type": "integer"}}), "want integer"},
		{"integer accepts whole", `{"i":2}`, obj(map[string]any{"i": map[string]any{"type": "integer"}}), ""},
		{"boolean", `{"b":"true"}`, obj(map[string]any{"b": map[string]any{"type": "boolean"}}), "want boolean"},
		{"boolean ok", `{"b":false}`, obj(map[string]any{"b": map[string]any{"type": "boolean"}}), ""},
		{"array", `{"a":{}}`, obj(map[string]any{"a": map[string]any{"type": "array"}}), "want array"},
		{"array ok", `{"a":[1]}`, obj(map[string]any{"a": map[string]any{"type": "array"}}), ""},
		{"enum", `"maybe"`, map[string]any{"enum": []any{"yes", "no"}}, "not one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainstSchema(json.RawMessage(tc.raw), tc.schema)
			if tc.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.err)
		})
	}
}

func TestStructured_NativeProviderError(t *testing.T) {
	_, err := Structured(context.Background(), &failingStructured{err: errors.New("rate limited")},
		StructuredRequest{Prompt: "x", Schema: yesNoSchema})
	assert.ErrorContains(t, err, "rate limited")
}

func TestRiskApprover_ScoresEveryToolWhenUnlisted(t *testing.T) {
	j := &riskJudge{answer: "yes", conf: 0.9}
	r := &RiskApprover{Inner: AllowAllApprover{}, Provider: j, Threshold: 0.8, FailClosed: true, Timeout: time.Second}
	d, _ := r.Approve(context.Background(), ApprovalRequest{ToolName: "Read", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionDeny, d)
	assert.Equal(t, int32(1), j.n.Load())
}
