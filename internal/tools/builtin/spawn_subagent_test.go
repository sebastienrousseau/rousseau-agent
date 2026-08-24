package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

// stubProvider is a minimal agent.Provider that returns a fixed
// end-of-turn reply so we can exercise the spawn plumbing without
// hitting a real LLM.
type stubProvider struct {
	name  string
	reply string
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: s.reply}},
		},
		StopReason: agent.StopEndTurn,
		Usage:      agent.Usage{InputTokens: 5, OutputTokens: 3},
	}, nil
}

func TestSpawnSubagentTool_Metadata(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	if got := tool.Name(); got != "spawn_subagent" {
		t.Errorf("Name() = %q, want %q", got, "spawn_subagent")
	}
	if tool.Description() == "" {
		t.Error("Description() empty")
	}
	schema := tool.InputSchema()
	if schema["type"] != "object" {
		t.Errorf("InputSchema type = %v, want object", schema["type"])
	}
	if _, ok := schema["properties"].(map[string]any)["tasks"]; !ok {
		t.Error("InputSchema missing tasks property")
	}
}

func TestSpawnSubagentTool_HappyPath(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 2})
	provider := &stubProvider{name: "stub", reply: "ok"}
	session := &agent.Session{ID: "s1"}

	ctx := toolcontext.WithProvider(
		toolcontext.WithSession(context.Background(), session),
		provider)

	input := json.RawMessage(`{
        "tasks": [
            {"prompt": "task 1"},
            {"prompt": "task 2"},
            {"prompt": "task 3"}
        ]
    }`)

	out, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var parsed struct {
		Summary struct {
			Total     int `json:"total"`
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"summary"`
		Tasks []struct {
			Index     int    `json:"index"`
			FinalText string `json:"final_text"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if parsed.Summary.Total != 3 {
		t.Errorf("Total = %d, want 3", parsed.Summary.Total)
	}
	if parsed.Summary.Succeeded != 3 {
		t.Errorf("Succeeded = %d, want 3", parsed.Summary.Succeeded)
	}
	if parsed.Summary.Failed != 0 {
		t.Errorf("Failed = %d, want 0", parsed.Summary.Failed)
	}
	for i, task := range parsed.Tasks {
		if task.FinalText != "ok" {
			t.Errorf("task[%d].FinalText = %q, want %q", i, task.FinalText, "ok")
		}
	}
}

func TestSpawnSubagentTool_MissingSession(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	provider := &stubProvider{name: "stub", reply: "ok"}

	// Provider set but no session — must fail with ErrSpawnMissingContext.
	ctx := toolcontext.WithProvider(context.Background(), provider)
	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"prompt":"x"}]}`))
	if !errors.Is(err, builtin.ErrSpawnMissingContext) {
		t.Errorf("expected ErrSpawnMissingContext, got %v", err)
	}
}

func TestSpawnSubagentTool_MissingProvider(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	session := &agent.Session{ID: "s1"}

	// Session set but no provider — must fail with ErrSpawnMissingContext.
	ctx := toolcontext.WithSession(context.Background(), session)
	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"prompt":"x"}]}`))
	if !errors.Is(err, builtin.ErrSpawnMissingContext) {
		t.Errorf("expected ErrSpawnMissingContext, got %v", err)
	}
}

func TestSpawnSubagentTool_EmptyTasks(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	session := &agent.Session{ID: "s1"}
	provider := &stubProvider{name: "stub", reply: "ok"}
	ctx := toolcontext.WithProvider(toolcontext.WithSession(context.Background(), session), provider)

	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[]}`))
	if err == nil {
		t.Error("expected error for empty tasks, got nil")
	}
}

func TestSpawnSubagentTool_TaskWithoutPrompt(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	session := &agent.Session{ID: "s1"}
	provider := &stubProvider{name: "stub", reply: "ok"}
	ctx := toolcontext.WithProvider(toolcontext.WithSession(context.Background(), session), provider)

	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"prompt":""}]}`))
	if err == nil {
		t.Error("expected error for empty prompt, got nil")
	}
}

func TestSpawnSubagentTool_CallerOverridesPolicy(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 1})
	session := &agent.Session{ID: "s1"}
	provider := &stubProvider{name: "stub", reply: "ok"}
	ctx := toolcontext.WithProvider(toolcontext.WithSession(context.Background(), session), provider)

	// Two tasks + max_concurrent override of 2 should complete without
	// serialising. We don't measure timing here (flaky under CI), just
	// that the override is accepted.
	out, err := tool.Execute(ctx, json.RawMessage(`{
        "tasks": [{"prompt": "a"}, {"prompt": "b"}],
        "max_concurrent": 2,
        "budget_tokens": 1000
    }`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}
