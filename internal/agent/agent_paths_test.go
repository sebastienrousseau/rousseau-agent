package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// --- shared fixtures --------------------------------------------------

// scriptedProvider replays a fixed script of responses and records the
// requests it saw, so tests can assert on what the loop actually sent.
type scriptedProvider struct {
	script []Response
	err    error
	seen   []Request
	i      int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Complete(_ context.Context, r Request) (Response, error) {
	p.seen = append(p.seen, r)
	if p.err != nil {
		return Response{}, p.err
	}
	if p.i >= len(p.script) {
		return Response{}, errors.New("scripted provider exhausted")
	}
	resp := p.script[p.i]
	p.i++
	return resp, nil
}

func toolUseResponse(id, name, input string) Response {
	return Response{
		Message: Message{
			Role: RoleAssistant,
			Content: []Content{{
				Kind:    ContentToolUse,
				ToolUse: &ToolUse{ID: id, Name: name, Input: json.RawMessage(input)},
			}},
		},
		StopReason: StopToolUse,
	}
}

func endTurnResponse(text string) Response {
	return Response{
		Message:    NewAssistantText(text),
		StopReason: StopEndTurn,
	}
}

func sessionWith(text string) *Session {
	s := NewSession("t")
	s.Append(NewUserText(text))
	return s
}

// recordingTool captures the input it was executed with so hook
// rewrites are observable through behaviour rather than mock counts.
type recordingTool struct {
	name string
	out  string
	err  error
	got  []string
}

func (r *recordingTool) Name() string                { return r.name }
func (r *recordingTool) Description() string         { return "recording" }
func (r *recordingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (r *recordingTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	r.got = append(r.got, string(in))
	return r.out, r.err
}

func registryWith(t *testing.T, tl tools.Tool) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(tl))
	return reg
}

// --- New ---------------------------------------------------------------

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	a := New(&scriptedProvider{script: []Response{endTurnResponse("ok")}},
		tools.NewRegistry(), nil, Options{})
	require.NotNil(t, a.logger)

	// The agent must still run with the substituted logger.
	final, err := a.Turn(context.Background(), sessionWith("hi"))
	require.NoError(t, err)
	assert.Equal(t, "ok", final.Content[0].Text)
}

// --- Turn error + telemetry paths -------------------------------------

func TestTurn_ProviderErrorIsWrapped(t *testing.T) {
	boom := errors.New("upstream 503")
	a := New(&scriptedProvider{err: boom}, tools.NewRegistry(), silentLogger(), Options{})

	_, err := a.Turn(context.Background(), sessionWith("hi"))
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "provider:")
}

func TestTurn_CompressorRewriteIsAppliedBeforeTheRequest(t *testing.T) {
	prov := &scriptedProvider{script: []Response{endTurnResponse("ok")}}
	// A compressor that collapses the history to a single message.
	compressor := CompressorFunc(func(_ context.Context, s *Session) (bool, error) {
		s.Messages = []Message{NewUserText("compressed")}
		return true, nil
	})
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{Compressor: compressor})

	s := sessionWith("original one")
	s.Append(NewAssistantText("original two"))
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	require.Len(t, prov.seen, 1)
	require.Len(t, prov.seen[0].Messages, 1)
	assert.Equal(t, "compressed", prov.seen[0].Messages[0].Content[0].Text)
}

// costSpy is a real CostRecorder that stores what it is handed.
type costSpy struct {
	events []CostEvent
	err    error
}

func (c *costSpy) Record(_ context.Context, e CostEvent) error {
	c.events = append(c.events, e)
	return c.err
}

func TestTurn_RecordsCostPerCompletion(t *testing.T) {
	prov := &scriptedProvider{script: []Response{{
		Message:    NewAssistantText("ok"),
		StopReason: StopEndTurn,
		Model:      "claude-test-1",
		Usage:      Usage{InputTokens: 11, OutputTokens: 7},
	}}}
	spy := &costSpy{}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{CostRecorder: spy})

	s := sessionWith("hi")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	require.Len(t, spy.events, 1)
	assert.Equal(t, s.ID, spy.events[0].SessionID)
	assert.Equal(t, "scripted", spy.events[0].Provider)
	assert.Equal(t, "claude-test-1", spy.events[0].Model)
	assert.Equal(t, 11, spy.events[0].Usage.InputTokens)
	assert.Equal(t, 7, spy.events[0].Usage.OutputTokens)
}

func TestTurn_CostRecorderFailureDoesNotAbortTheTurn(t *testing.T) {
	prov := &scriptedProvider{script: []Response{endTurnResponse("still fine")}}
	spy := &costSpy{err: errors.New("disk full")}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{CostRecorder: spy})

	final, err := a.Turn(context.Background(), sessionWith("hi"))
	require.NoError(t, err)
	assert.Equal(t, "still fine", final.Content[0].Text)
	assert.Len(t, spy.events, 1)
}

// --- runTools ----------------------------------------------------------

func TestTurn_NonToolUseContentBlocksAreSkipped(t *testing.T) {
	tool := &recordingTool{name: "echo", out: "pong"}
	mixed := Response{
		Message: Message{
			Role: RoleAssistant,
			Content: []Content{
				{Kind: ContentText, Text: "let me check"},
				{Kind: ContentToolUse, ToolUse: nil}, // malformed: no payload
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}},
			},
		},
		StopReason: StopToolUse,
	}
	prov := &scriptedProvider{script: []Response{mixed, endTurnResponse("done")}}
	a := New(prov, registryWith(t, tool), silentLogger(), Options{})

	s := sessionWith("go")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	// Exactly one tool result — the text and nil-payload blocks were skipped.
	results := s.Messages[2].Content
	require.Len(t, results, 1)
	assert.Equal(t, "pong", results[0].ToolResult.Output)
}

func TestTurn_ApproverDenialWithoutReasonUsesDefaultText(t *testing.T) {
	tool := &recordingTool{name: "rm", out: "deleted"}
	prov := &scriptedProvider{script: []Response{
		toolUseResponse("c1", "rm", `{}`),
		endTurnResponse("understood"),
	}}
	silent := ApproverFunc(func(context.Context, ApprovalRequest) (Decision, string) {
		return DecisionDeny, "" // no reason supplied
	})
	a := New(prov, registryWith(t, tool), silentLogger(), Options{Approver: silent})

	s := sessionWith("delete everything")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	res := s.Messages[2].Content[0].ToolResult
	assert.True(t, res.IsError)
	assert.Equal(t, "tool call blocked: denied by policy", res.Output)
	assert.Empty(t, tool.got, "denied tool must not run")
}

// --- hooks -------------------------------------------------------------

// stubHooks is a hooks.Runner returning a canned verdict.
type stubHooks struct {
	verdict hooks.Verdict
	err     error
	seen    [][]byte
}

func (h *stubHooks) Run(_ context.Context, _ hooks.Event, payload []byte) (hooks.Verdict, error) {
	h.seen = append(h.seen, payload)
	return h.verdict, h.err
}

func TestTurn_HookAllowsToolAndSeesThePayload(t *testing.T) {
	tool := &recordingTool{name: "echo", out: "pong"}
	hk := &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionAllow}}
	prov := &scriptedProvider{script: []Response{
		toolUseResponse("c1", "echo", `{"n":1}`),
		endTurnResponse("done"),
	}}
	a := New(prov, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

	s := sessionWith("go")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	require.Len(t, hk.seen, 1)
	var payload hooks.PreToolUsePayload
	require.NoError(t, json.Unmarshal(hk.seen[0], &payload))
	assert.Equal(t, hooks.EventPreToolUse, payload.Event)
	assert.Equal(t, s.ID, payload.SessionID)
	assert.Equal(t, "echo", payload.ToolName)
	assert.JSONEq(t, `{"n":1}`, string(payload.Input))
	assert.Equal(t, []string{`{"n":1}`}, tool.got)
}

func TestTurn_HookDenyBlocksTheTool(t *testing.T) {
	tests := []struct {
		name    string
		verdict hooks.Verdict
		want    string
	}{
		{
			name:    "explicit reason",
			verdict: hooks.Verdict{Decision: hooks.DecisionDeny, Reason: "no writes on friday"},
			want:    "tool call blocked by hook: no writes on friday",
		},
		{
			name:    "default reason",
			verdict: hooks.Verdict{Decision: hooks.DecisionDeny},
			want:    "tool call blocked by hook: denied by hook",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool := &recordingTool{name: "write", out: "written"}
			prov := &scriptedProvider{script: []Response{
				toolUseResponse("c1", "write", `{}`),
				endTurnResponse("ok"),
			}}
			a := New(prov, registryWith(t, tool), silentLogger(),
				Options{Hooks: &stubHooks{verdict: tc.verdict}})

			s := sessionWith("write a file")
			_, err := a.Turn(context.Background(), s)
			require.NoError(t, err)

			res := s.Messages[2].Content[0].ToolResult
			assert.True(t, res.IsError)
			assert.Equal(t, tc.want, res.Output)
			assert.Empty(t, tool.got, "hook-denied tool must not run")
		})
	}
}

func TestTurn_HookRunErrorFailsOpen(t *testing.T) {
	tool := &recordingTool{name: "echo", out: "pong"}
	hk := &stubHooks{err: errors.New("hook binary missing")} // zero Verdict → not a deny
	prov := &scriptedProvider{script: []Response{
		toolUseResponse("c1", "echo", `{}`),
		endTurnResponse("done"),
	}}
	a := New(prov, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

	s := sessionWith("go")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, []string{`{}`}, tool.got, "a failing hook must not block the tool")
}

func TestTurn_HookModifyRewritesToolInput(t *testing.T) {
	tests := []struct {
		name     string
		modified string
		want     string
	}{
		{name: "valid JSON replaces input", modified: `{"path":"/safe"}`, want: `{"path":"/safe"}`},
		{name: "invalid JSON is ignored", modified: `not-json`, want: `{"path":"/etc/shadow"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool := &recordingTool{name: "read", out: "contents"}
			hk := &stubHooks{verdict: hooks.Verdict{
				Decision: hooks.DecisionModify,
				Modified: json.RawMessage(tc.modified),
			}}
			prov := &scriptedProvider{script: []Response{
				toolUseResponse("c1", "read", `{"path":"/etc/shadow"}`),
				endTurnResponse("done"),
			}}
			a := New(prov, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

			_, err := a.Turn(context.Background(), sessionWith("read a file"))
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, tool.got)
		})
	}
}

func TestTurn_HookModifyWithEmptyPayloadLeavesInputAlone(t *testing.T) {
	tool := &recordingTool{name: "read", out: "contents"}
	hk := &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionModify}}
	prov := &scriptedProvider{script: []Response{
		toolUseResponse("c1", "read", `{"path":"/a"}`),
		endTurnResponse("done"),
	}}
	a := New(prov, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

	_, err := a.Turn(context.Background(), sessionWith("read"))
	require.NoError(t, err)
	assert.Equal(t, []string{`{"path":"/a"}`}, tool.got)
}

func TestTurn_HookPayloadMarshalFailureFailsOpen(t *testing.T) {
	tool := &recordingTool{name: "echo", out: "pong"}
	hk := &stubHooks{verdict: hooks.Verdict{Decision: hooks.DecisionDeny, Reason: "never reached"}}
	// A ToolUse carrying malformed raw JSON cannot be marshalled into
	// the hook payload; the loop logs and proceeds without the hook.
	prov := &scriptedProvider{script: []Response{
		toolUseResponse("c1", "echo", `this is not json`),
		endTurnResponse("done"),
	}}
	a := New(prov, registryWith(t, tool), silentLogger(), Options{Hooks: hk})

	s := sessionWith("go")
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Empty(t, hk.seen, "payload never got built")
	assert.Equal(t, []string{`this is not json`}, tool.got)
	assert.False(t, s.Messages[2].Content[0].ToolResult.IsError)
}

// --- message -----------------------------------------------------------

func TestNewUserImage(t *testing.T) {
	m := NewUserImage("image/png", []byte{0x89, 'P', 'N', 'G'}, "whatsapp")
	assert.Equal(t, RoleUser, m.Role)
	require.Len(t, m.Content, 1)
	assert.Equal(t, ContentImage, m.Content[0].Kind)
	require.NotNil(t, m.Content[0].Image)
	assert.Equal(t, "image/png", m.Content[0].Image.MediaType)
	assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, m.Content[0].Image.Data)
	assert.Equal(t, "whatsapp", m.Content[0].Image.Source)
	assert.False(t, m.CreatedAt.IsZero())
}

// --- approver ----------------------------------------------------------

func TestPatternApprover_CustomDenyReason(t *testing.T) {
	p := &PatternApprover{
		Deny:       []PatternRule{{ToolName: "bash"}},
		DenyReason: "bash is disabled in this deployment",
	}
	decision, reason := p.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionDeny, decision)
	assert.Equal(t, "bash is disabled in this deployment", reason)

	// The same reason is used for the default-deny fall-through.
	decision, reason = p.Approve(context.Background(), ApprovalRequest{ToolName: "read"})
	assert.Equal(t, DecisionDeny, decision)
	assert.Equal(t, "bash is disabled in this deployment", reason)
}

// --- compressor --------------------------------------------------------

func TestLLMCompressor_DefaultKeepRecent(t *testing.T) {
	prov := &scriptedProvider{script: []Response{endTurnResponse("summary text")}}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 10} // KeepRecent unset → 8

	s := NewSession("x")
	for i := 0; i < 12; i++ {
		s.Append(NewUserText("message " + itoa(i)))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, changed)
	// 1 synthetic summary + the 8 most recent verbatim messages.
	require.Len(t, s.Messages, 9)
	assert.True(t, strings.HasPrefix(s.Messages[0].Content[0].Text, DefaultCompressorMarker))
	assert.Contains(t, s.Messages[0].Content[0].Text, "summary of prior 4 messages")
	assert.Equal(t, "message 11", s.Messages[8].Content[0].Text)
}

func TestLLMCompressor_KeepRecentCoversWholeSessionIsNoop(t *testing.T) {
	prov := &scriptedProvider{script: []Response{endTurnResponse("unused")}}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 2, KeepRecent: 50}

	s := NewSession("x")
	for i := 0; i < 5; i++ {
		s.Append(NewUserText("m" + itoa(i)))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Len(t, s.Messages, 5)
	assert.Empty(t, prov.seen, "provider must not be called")
}

func TestLLMCompressor_HeadAlreadyCompressedOnEmptySession(t *testing.T) {
	c := &LLMCompressor{}
	assert.False(t, c.headAlreadyCompressed(NewSession("x")))
}

func TestLLMCompressor_ProviderWithNoTextErrors(t *testing.T) {
	// Response carries only a tool-use block — no summary text.
	prov := &scriptedProvider{script: []Response{toolUseResponse("c1", "x", `{}`)}}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 3, KeepRecent: 1}

	s := NewSession("x")
	for i := 0; i < 5; i++ {
		s.Append(NewUserText("m" + itoa(i)))
	}
	_, err := c.Compress(context.Background(), s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned no text")
}

// --- recall ------------------------------------------------------------

func TestFTSRecall_AllHitsFilteredReturnsEmpty(t *testing.T) {
	r := &FTSRecall{
		Searcher: &stubSearcher{hits: []SearchHit{
			{SessionID: "same", Title: "a", Snippet: "s"},
		}},
		SkipSessionID: func(s *Session) string { return s.ID },
	}
	s := NewSession("x")
	s.ID = "same"
	s.Append(NewUserText("deployment pipeline configuration"))
	assert.Empty(t, r.SystemAppendix(context.Background(), s))
}

func TestLastUserText_SkipsAssistantAndJoinsBlocks(t *testing.T) {
	s := NewSession("x")
	s.Append(Message{Role: RoleUser, Content: []Content{
		{Kind: ContentText, Text: "first line"},
		{Kind: ContentText, Text: "second line"},
		{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "i", Name: "n"}},
	}})
	s.Append(NewAssistantText("assistant reply"))

	got, ok := lastUserText(s)
	require.True(t, ok)
	assert.Equal(t, "first line\nsecond line", got)
}

func TestLastUserText_UserMessageWithoutTextIsSkipped(t *testing.T) {
	s := NewSession("x")
	s.Append(NewUserText("earlier text"))
	s.Append(Message{Role: RoleUser, Content: []Content{
		{Kind: ContentToolResult, ToolResult: &ToolResult{ToolUseID: "i", Output: "out"}},
	}})

	got, ok := lastUserText(s)
	require.True(t, ok)
	assert.Equal(t, "earlier text", got)
}

// --- structured --------------------------------------------------------

func TestStructured_UnmarshalableSchemaErrors(t *testing.T) {
	p := &structuredProvider{reply: `{}`}
	_, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "x",
		Schema: map[string]any{"bad": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent: schema:")
}

func TestStructured_LongUnparseableReplyIsTruncatedInTheError(t *testing.T) {
	reply := strings.Repeat("prose ", 100) // >200 chars, no JSON
	p := &structuredProvider{reply: reply}
	_, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "x",
		Schema: map[string]any{"type": "object"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "…", "long replies are elided")
	assert.Less(t, len(err.Error()), len(reply))
}

func TestExtractJSON_FenceVariants(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "language-tagged fence",
			in:   "```json\n{\"a\": 1}\n```",
			want: `{"a": 1}`,
		},
		{
			name: "bare fence",
			in:   "```\n[1, 2]\n```",
			want: `[1, 2]`,
		},
		{
			name: "unterminated fence",
			in:   "```json\n{\"a\": 2}",
			want: `{"a": 2}`,
		},
		{
			name: "fence with no newline at all",
			in:   "```{\"a\": 3}```",
			want: `{"a": 3}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractJSON(tc.in)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestExtractJSON_CloserBeforeOpenerErrors(t *testing.T) {
	_, err := extractJSON("} then {")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching JSON close")
}

func TestTruncateForError(t *testing.T) {
	assert.Equal(t, "short", truncateForError("short", 200))
	assert.Equal(t, "abc…", truncateForError("abcdef", 3))
}
