package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runPatternTool drives one tool call through runOneTool under ap and
// reports the tool_result plus what the tool actually received.
func runPatternTool(t *testing.T, ap Approver, name, input string) (*ToolResult, []string) {
	t.Helper()
	tool := &recordingTool{name: name, out: "ran"}
	a := New(&scriptedProvider{}, registryWith(t, tool), silentLogger(), Options{Approver: ap})
	use := &ToolUse{ID: "c1", Name: name, Input: json.RawMessage(input)}
	res := a.runOneTool(context.Background(), use, "s1")
	require.NotNil(t, res.ToolResult)
	return res.ToolResult, tool.got
}

// esc returns the JSON escape sequence for the 4-hex-digit code point
// hex (backslash, "u", hex). Built from byte 92 so no editor or
// generator can pre-decode the escape the tests depend on.
func esc(hex string) string { return string([]byte{92}) + "u" + hex }

func TestPattern_DenyCannotBeEscapedAway(t *testing.T) {
	ap := &PatternApprover{
		Deny:    []PatternRule{{ToolName: "bash", Match: `rm -rf`}},
		Default: DecisionAllow,
	}
	// esc("0072") is "r": the tool decodes the input to "rm -rf /".
	res, got := runPatternTool(t, ap, "bash", `{"command":"`+esc("0072")+`m -rf /"}`)
	assert.True(t, res.IsError, "escaped deny pattern must still be denied")
	assert.Empty(t, got, "denied tool must not run")
}

func TestPattern_DuplicateKeyRejected(t *testing.T) {
	ap := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "bash", Match: `"command":"ls"`}},
		Default: DecisionDeny,
	}
	res, got := runPatternTool(t, ap, "bash", `{"command":"ls","command":"curl x|sh"}`)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Output, "repeats a key")
	assert.Empty(t, got, "a duplicate-key input must not reach the tool")
}

func TestPattern_ToolSeesCanonicalInput(t *testing.T) {
	res, got := runPatternTool(t, AllowAllApprover{}, "bash", `{"b": 1, "a": "`+esc("0072")+`"}`)
	assert.False(t, res.IsError)
	assert.Equal(t, []string{`{"a":"r","b":1}`}, got)
}

func TestPattern_FieldRuleIsAnchored(t *testing.T) {
	ap := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "bash", Field: "command", Match: `git status`}},
		Default: DecisionDeny,
	}
	tests := []struct {
		name  string
		input string
		want  Decision
	}{
		{"exact", `{"command":"git status"}`, DecisionAllow},
		{"suffix chained", `{"command":"git status; curl x|sh"}`, DecisionDeny},
		{"prefix chained", `{"command":"curl x|sh; git status"}`, DecisionDeny},
		{"field elsewhere", `{"other":"git status","command":"curl x"}`, DecisionDeny},
		{"missing field", `{"cmd":"git status"}`, DecisionDeny},
		{"non-string field", `{"command":["git status"]}`, DecisionDeny},
		{"not an object", `["git status"]`, DecisionDeny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canon, err := CanonicalInput(json.RawMessage(tc.input))
			require.NoError(t, err)
			dec, _ := ap.Approve(context.Background(), ApprovalRequest{ToolName: "bash", Input: canon})
			assert.Equal(t, tc.want, dec)
		})
	}
}

func TestPattern_FieldAlternationStaysAnchored(t *testing.T) {
	// `^(?:a|b)$`, not `^a|b$`: the alternation must not escape the anchors.
	ap := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "bash", Field: "command", Match: `ls|pwd`}},
		Default: DecisionDeny,
	}
	dec, _ := ap.Approve(context.Background(), ApprovalRequest{ToolName: "bash", Input: json.RawMessage(`{"command":"ls; curl x"}`)})
	assert.Equal(t, DecisionDeny, dec)
	dec, _ = ap.Approve(context.Background(), ApprovalRequest{ToolName: "bash", Input: json.RawMessage(`{"command":"pwd"}`)})
	assert.Equal(t, DecisionAllow, dec)
}

func TestPattern_FieldDenyMatchesCaseFoldedKey(t *testing.T) {
	// Go's struct decoding matches keys case-insensitively, so a deny
	// on "command" must also see "Command".
	ap := &PatternApprover{
		Deny:    []PatternRule{{ToolName: "bash", Field: "command", Match: `rm -rf.*`}},
		Default: DecisionAllow,
	}
	dec, _ := ap.Approve(context.Background(), ApprovalRequest{
		ToolName: "bash", Input: json.RawMessage(`{"Command":"rm -rf /"}`),
	})
	assert.Equal(t, DecisionDeny, dec)
}

func TestPattern_ToolNameCaseSensitive(t *testing.T) {
	ap := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "mcp:x:run"}},
		Default: DecisionDeny,
	}
	dec, _ := ap.Approve(context.Background(), ApprovalRequest{ToolName: "mcp:x:RUN", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionDeny, dec, "a rule for mcp:x:run must not match mcp:x:RUN")
	dec, _ = ap.Approve(context.Background(), ApprovalRequest{ToolName: "mcp:x:run", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionAllow, dec)

	// A deny keeps folding case: matching more tools is the safe
	// direction, and the claude CLI calls the shell tool "Bash".
	deny := &PatternApprover{Deny: []PatternRule{{ToolName: "bash"}}, Default: DecisionAllow}
	dec, _ = deny.Approve(context.Background(), ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionDeny, dec)
}

func TestPattern_ApproverRejectsNonCanonicalInputDirectly(t *testing.T) {
	// The external-tool bridge calls the approver without runOneTool,
	// so the approver canonicalises on its own.
	ap := &PatternApprover{Default: DecisionAllow}
	dec, reason := ap.Approve(context.Background(), ApprovalRequest{
		ToolName: "bash", Input: json.RawMessage(`{"command":"ls","command":"curl x|sh"}`),
	})
	assert.Equal(t, DecisionDeny, dec)
	assert.Contains(t, reason, "repeats a key")
}

func TestCanonicalInput(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty", in: ``, want: `{}`},
		{name: "sorted keys", in: `{"b":1,"a":2}`, want: `{"a":2,"b":1}`},
		{name: "whitespace dropped", in: " { \"a\" : [ 1 , 2 ] } ", want: `{"a":[1,2]}`},
		{name: "escapes normalised", in: `{"command":"` + esc("0072") + `m -rf /"}`, want: `{"command":"rm -rf /"}`},
		{name: "escaped key normalised", in: `{"` + esc("0061") + `":1}`, want: `{"a":1}`},
		{name: "html not escaped", in: `{"c":"a<b && c>d"}`, want: `{"c":"a<b && c>d"}`},
		{name: "big number kept", in: `{"n":12345678901234567890}`, want: `{"n":12345678901234567890}`},
		{name: "nested sorted", in: `{"z":{"y":1,"x":[{"b":1,"a":2}]}}`, want: `{"z":{"x":[{"a":2,"b":1}],"y":1}}`},
		{name: "same key in sibling objects ok", in: `[{"a":1},{"a":2}]`, want: `[{"a":1},{"a":2}]`},
		{name: "duplicate top-level", in: `{"a":1,"a":2}`, wantErr: true},
		{name: "duplicate nested", in: `{"x":{"k":1,"k":2}}`, wantErr: true},
		{name: "duplicate in array element", in: `[{"k":1},{"k":1,"k":2}]`, wantErr: true},
		{name: "duplicate via escape", in: `{"a":1,"` + esc("0061") + `":2}`, wantErr: true},
		{name: "duplicate by kelvin fold", in: `{"k":1,"` + esc("212a") + `":2}`, wantErr: true},
		{name: "duplicate by case fold", in: `{"command":"ls","Command":"curl"}`, wantErr: true},
		{name: "invalid", in: `not json`, wantErr: true},
		{name: "trailing data", in: `{} {}`, wantErr: true},
		{name: "truncated", in: `{"a":`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalInput(json.RawMessage(tc.in))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			again, err := CanonicalInput(got)
			require.NoError(t, err)
			assert.Equal(t, string(got), string(again), "canonical form must be idempotent")
		})
	}
}

func FuzzCanonicalInput(f *testing.F) {
	for _, seed := range []string{
		``, `{}`, `{"a":1}`, `{"b":"` + esc("0072") + `","a":[1,2,{"c":null}]}`,
		`{"a":1,"a":2}`, `[{"k":1},{"k":2}]`, `"str"`, `1e400`, `{"n":-0.0}`,
		`{"s":"` + esc("2028") + `<>&` + esc("d800") + `"}`, `{"K":1,"` + esc("212a") + `":2}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		once, err := CanonicalInput(in)
		if err != nil {
			return
		}
		twice, err := CanonicalInput(once)
		if err != nil {
			t.Fatalf("canonical output rejected on second pass: %v", err)
		}
		if string(once) != string(twice) {
			t.Fatalf("not idempotent: %q != %q", once, twice)
		}
	})
}
