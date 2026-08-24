package subagent_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	pkgsub "github.com/sebastienrousseau/rousseau-agent/pkg/agent/subagent"
)

type stubProvider struct{}

func (*stubProvider) Name() string { return "stub" }
func (*stubProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "hi"}},
		},
		StopReason: agent.StopEndTurn,
		Usage:      agent.Usage{InputTokens: 5, OutputTokens: 3},
	}, nil
}

func TestSpawn_ErrNoTasks(t *testing.T) {
	_, err := pkgsub.Spawn(context.Background(),
		&agent.Session{ID: "s1"}, &stubProvider{}, nil, pkgsub.Policy{}, slog.Default())
	assert.ErrorIs(t, err, pkgsub.ErrNoTasks)
}

func TestSpawn_ErrOverBudgetSentinel(t *testing.T) {
	// The sentinel is re-exported unchanged; verify pkg identity.
	assert.Equal(t, "subagent: over budget", pkgsub.ErrOverBudget.Error())
	assert.True(t, errors.Is(pkgsub.ErrOverBudget, pkgsub.ErrOverBudget))
}

func TestSpawn_RunsAllTasks(t *testing.T) {
	tasks := []pkgsub.Task{
		{Prompt: "a"},
		{Prompt: "b"},
	}
	results, err := pkgsub.Spawn(context.Background(),
		&agent.Session{ID: "s1"}, &stubProvider{}, tasks,
		pkgsub.Policy{MaxConcurrent: 2}, slog.Default())
	require.NoError(t, err)
	assert.Len(t, results, 2)
	for _, r := range results {
		assert.Equal(t, "hi", r.FinalText)
	}
}

func TestTypeAliases_Assignable(t *testing.T) {
	// Composite-literal Task via the pkg alias must work — verifies
	// the alias is transparent. Using the pkgsub-prefixed constructor
	// is what actually exercises the alias; the `var` declarations
	// were pinned to the alias type but staticcheck flags that as
	// redundant, so use `:=` and let inference do it.
	task := pkgsub.Task{Prompt: "x"}
	policy := pkgsub.Policy{MaxConcurrent: 3}
	result := pkgsub.Result{FinalText: "y"}
	assert.Equal(t, "x", task.Prompt)
	assert.Equal(t, 3, policy.MaxConcurrent)
	assert.Equal(t, "y", result.FinalText)
}

func TestAggregateJSON_ProducesJSON(t *testing.T) {
	results := []pkgsub.Result{
		{TaskIndex: 0, FinalText: "one"},
		{TaskIndex: 1, FinalText: "two"},
	}
	// AggregateJSON returns an agent.Content whose ToolResult carries
	// the compact-JSON rendering of every task outcome. The second
	// arg is a byte-cap (0 = default budget).
	content := pkgsub.AggregateJSON(results, 0)
	require.NotNil(t, content.ToolResult)
	assert.Contains(t, content.ToolResult.Output, "one")
	assert.Contains(t, content.ToolResult.Output, "two")
}
