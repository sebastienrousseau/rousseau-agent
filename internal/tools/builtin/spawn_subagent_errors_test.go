package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

// failingProvider always errors so a task's Result carries an Err.
type failingProvider struct{ err error }

func (*failingProvider) Name() string { return "failing" }

func (p *failingProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, p.err
}

func TestSpawnSubagentTool_InvalidJSON(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":`))
	assert.ErrorContains(t, err, "spawn_subagent: parse input")
}

func TestSpawnSubagentTool_WrongContextValueTypes(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	input := json.RawMessage(`{"tasks":[{"prompt":"x"}]}`)

	t.Run("session of the wrong type", func(t *testing.T) {
		ctx := toolcontext.WithProvider(
			toolcontext.WithSession(context.Background(), "not-a-session"),
			&stubProvider{name: "stub", reply: "ok"})
		_, err := tool.Execute(ctx, input)
		assert.ErrorContains(t, err, "session context value has type string")
	})

	t.Run("provider of the wrong type", func(t *testing.T) {
		ctx := toolcontext.WithProvider(
			toolcontext.WithSession(context.Background(), &agent.Session{ID: "s1"}),
			42)
		_, err := tool.Execute(ctx, input)
		assert.ErrorContains(t, err, "provider context value has type int")
	})
}

// TestSpawnSubagentTool_SpawnRejectsNilSession drives the error return
// from subagent.Spawn itself: a typed-nil *agent.Session type-asserts
// fine but is not a usable parent.
func TestSpawnSubagentTool_SpawnRejectsNilSession(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	ctx := toolcontext.WithProvider(
		toolcontext.WithSession(context.Background(), (*agent.Session)(nil)),
		&stubProvider{name: "stub", reply: "ok"})
	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"prompt":"x"}]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spawn_subagent: ")
	assert.Contains(t, err.Error(), "nil parent session")
}

// TestSpawnSubagentTool_ReportsPerTaskFailures asserts a provider
// failure is reported per task rather than failing the whole call.
func TestSpawnSubagentTool_ReportsPerTaskFailures(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 2})
	ctx := toolcontext.WithProvider(
		toolcontext.WithSession(context.Background(), &agent.Session{ID: "s1"}),
		&failingProvider{err: errors.New("upstream 503")})

	out, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"prompt":"a"},{"prompt":"b"}]}`))
	require.NoError(t, err, "a failing sub-agent must not fail the tool call")

	var parsed struct {
		Summary struct {
			Total     int `json:"total"`
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"summary"`
		Tasks []struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, 2, parsed.Summary.Total)
	assert.Equal(t, 0, parsed.Summary.Succeeded)
	assert.Equal(t, 2, parsed.Summary.Failed)
	require.Len(t, parsed.Tasks, 2)
	for _, task := range parsed.Tasks {
		assert.Contains(t, task.Error, "upstream 503")
	}
}
