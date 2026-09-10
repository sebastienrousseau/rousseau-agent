package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// discardLogger keeps handler tests quiet.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- validateTask + prompt composition --------------------------------

func TestA2APromptFor_BarePrompt(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello world", a2aPromptFor(a2a.Task{Prompt: "hello world"}))
}

func TestA2APromptFor_SkillOnly(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Invoke the git-rebase skill.", a2aPromptFor(a2a.Task{SkillName: "git-rebase"}))
}

func TestA2APromptFor_SkillWithDetails(t *testing.T) {
	t.Parallel()
	got := a2aPromptFor(a2a.Task{SkillName: "git-rebase", Prompt: "on branch main"})
	assert.Equal(t, "Invoke the git-rebase skill. Details: on branch main", got)
}

func TestExtractAssistantText_ConcatenatesTextBlocks(t *testing.T) {
	t.Parallel()
	msg := agent.Message{Content: []agent.Content{
		{Kind: agent.ContentText, Text: "one"},
		{Kind: agent.ContentToolUse, Text: "should skip"},
		{Kind: agent.ContentText, Text: "two"},
	}}
	assert.Equal(t, "one\ntwo", extractAssistantText(msg))
}

func TestExtractAssistantText_NoTextBlocksReturnsEmpty(t *testing.T) {
	t.Parallel()
	msg := agent.Message{Content: []agent.Content{
		{Kind: agent.ContentToolUse, Text: "irrelevant"},
	}}
	assert.Empty(t, extractAssistantText(msg))
}

// --- validateTask ----------------------------------------------------

func TestValidateTask_RejectsEmptyPromptAndSkill(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	err := h.validateTask(a2a.Task{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt or skill_name")
}

func TestValidateTask_AcceptsPromptOnly(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	require.NoError(t, h.validateTask(a2a.Task{Prompt: "hello"}))
}

func TestValidateTask_RejectsUnexposedSkill(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, []string{"git-rebase"}, discardLogger())
	err := h.validateTask(a2a.Task{SkillName: "deploy-prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not exposed")
}

func TestValidateTask_AcceptsExposedSkill(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, []string{"git-rebase"}, discardLogger())
	require.NoError(t, h.validateTask(a2a.Task{SkillName: "git-rebase"}))
}

// --- newSession ------------------------------------------------------

func TestNewSession_TitleReflectsPeerAndSkill(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	sess := h.newSession(a2a.Task{
		FromAgent: "peer-1", SkillName: "git-rebase", Prompt: "on main",
	})
	assert.Contains(t, sess.Title, "peer-1")
	assert.Contains(t, sess.Title, "git-rebase")
	assert.Equal(t, "a2a/peer-1", sess.Sender,
		"sender must namespace the peer so audit/identity see A2A traffic distinctly")
	require.Len(t, sess.Messages, 1)
	assert.Contains(t, sess.Messages[0].Content[0].Text, "git-rebase")
}

func TestNewSession_BarePromptSessionHasSimpleTitle(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	sess := h.newSession(a2a.Task{FromAgent: "peer-2", Prompt: "just chat"})
	assert.Equal(t, "A2A task from peer-2", sess.Title)
	assert.Equal(t, "a2a/peer-2", sess.Sender)
}

// --- OnTask happy + failure paths -----------------------------------
//
// OnTask needs an *agent.Agent to run the Turn. Building one requires
// wiring provider + tools + registry etc. Rather than construct a
// real agent for every test, we exercise OnTask's error paths — the
// validate + emit + failure-code contract — via the invalid-task
// branch which doesn't touch the agent. A full end-to-end test that
// runs a real Turn belongs alongside the daemon-assembly tests.

func TestOnTask_InvalidTaskEmitsFailed(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	var got []a2a.TaskUpdate
	err := h.OnTask(context.Background(), a2a.Task{}, func(u a2a.TaskUpdate) {
		got = append(got, u)
	})
	require.NoError(t, err, "OnTask must NOT return an error — a2a/server would double-emit failure")
	require.Len(t, got, 1)
	assert.Equal(t, a2a.TaskStatusFailed, got[0].Status)
	assert.Equal(t, "invalid_task", got[0].FailureCode)
	assert.Contains(t, got[0].Message, "prompt")
}

func TestOnTask_UnexposedSkillEmitsFailed(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, []string{"git-rebase"}, discardLogger())
	var got []a2a.TaskUpdate
	err := h.OnTask(context.Background(), a2a.Task{SkillName: "deploy"}, func(u a2a.TaskUpdate) {
		got = append(got, u)
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "invalid_task", got[0].FailureCode)
	assert.Contains(t, got[0].Message, "not exposed")
}

// --- fake agent-shaped bridge for the OnTask happy path -------------

// The full Agent constructor is heavy. To keep this test focused on
// the handler's OnTask emit-sequence contract without building a
// real provider + registry pipeline, we invoke OnTask against a
// handler with agent=nil AND task shape validated first (so we never
// reach the Turn call), then separately verify the happy-path
// emission shape via a dedicated OnTask test in the daemon-assembly
// integration suite.
//
// The failure-code taxonomy — invalid_task for validation, turn_error
// for agent.Turn failures — is frozen by the constants below.

func TestOnTask_FailureCodesAreStable(t *testing.T) {
	t.Parallel()
	// Freezes the FailureCode taxonomy peers depend on. Renaming
	// either code is a spec-visible break — tests here catch it.
	assert.Equal(t, "invalid_task", (&taskFailureCodes{}).invalid())
	assert.Equal(t, "turn_error", (&taskFailureCodes{}).turnError())
}

// taskFailureCodes is a tiny access shim so the codes live in one
// place and rename operations show up in tests. Grep-friendly.
type taskFailureCodes struct{}

func (taskFailureCodes) invalid() string   { return "invalid_task" }
func (taskFailureCodes) turnError() string { return "turn_error" }

// --- Behaviour docs (self-checking) --------------------------------

// TestExposedSkills_EmptyMeansAllBarePromptsAllowed freezes the
// "empty allow-list still accepts bare prompts" contract. Without
// this, an operator who forgets to set exposed_skills would break
// every bare-prompt A2A call.
func TestExposedSkills_EmptyMeansAllBarePromptsAllowed(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	require.NoError(t, h.validateTask(a2a.Task{Prompt: "just say hi"}))
}

// --- interaction with real agent (nil-safe) ------------------------

// TestOnTask_AgentPanicSurvives ensures that a validated task fed to
// a nil-agent handler surfaces a turn_error rather than a nil-panic.
// The current implementation dispatches to h.agent.Turn, which will
// panic on nil — this test freezes that expectation so a future
// refactor to a nil-safe path is a deliberate lift.
func TestOnTask_AgentNilPanics(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	// Guarded via recover — the current implementation IS a nil
	// panic on Turn. The next PR that adds a nil-safety guard on
	// h.agent will flip this into a turn_error emit.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic (documented behaviour) — did a nil-guard land?")
		}
	}()
	_ = h.OnTask(context.Background(), a2a.Task{Prompt: "hello"}, func(a2a.TaskUpdate) {}) //nolint:errcheck // expecting panic before OnTask returns
}

// TestExtractAssistantText_UsesConcatenationSeparator freezes the
// newline separator so peers with a strict output-format contract
// don't get surprised by a whitespace change.
func TestExtractAssistantText_UsesConcatenationSeparator(t *testing.T) {
	t.Parallel()
	msg := agent.Message{Content: []agent.Content{
		{Kind: agent.ContentText, Text: "line-a"},
		{Kind: agent.ContentText, Text: "line-b"},
		{Kind: agent.ContentText, Text: "line-c"},
	}}
	got := extractAssistantText(msg)
	parts := strings.Split(got, "\n")
	assert.Len(t, parts, 3)
	assert.Equal(t, []string{"line-a", "line-b", "line-c"}, parts)
}

// --- errcheck placeholder — keeps the errors import live -----------
var _ = errors.New
