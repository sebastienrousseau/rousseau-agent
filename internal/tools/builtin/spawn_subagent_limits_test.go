package builtin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

// recordingProvider records what each sub-agent call saw (system
// prompt, deadline, concurrency) so tests can assert the limits the
// tool actually handed to subagent.Spawn.
type recordingProvider struct {
	usage agent.Usage
	stop  agent.StopReason
	hold  time.Duration

	mu          sync.Mutex
	calls       int
	inflight    int
	maxInflight int
	systems     []string
	remaining   []time.Duration
}

func (*recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	p.mu.Lock()
	p.calls++
	p.inflight++
	p.maxInflight = max(p.maxInflight, p.inflight)
	p.systems = append(p.systems, req.System)
	if dl, ok := ctx.Deadline(); ok {
		p.remaining = append(p.remaining, time.Until(dl))
	}
	p.mu.Unlock()

	if p.hold > 0 {
		select {
		case <-time.After(p.hold):
		case <-ctx.Done():
		}
	}

	p.mu.Lock()
	p.inflight--
	p.mu.Unlock()

	stop := p.stop
	if stop == "" {
		stop = agent.StopEndTurn
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "ok"}},
		},
		StopReason: stop,
		Usage:      p.usage,
	}, nil
}

type limitsOutcome struct {
	Tasks []struct {
		Index int    `json:"index"`
		Turns int    `json:"turns"`
		Error string `json:"error"`
	} `json:"tasks"`
}

func spawnCtx(p agent.Provider) context.Context {
	return toolcontext.WithProvider(
		toolcontext.WithSession(context.Background(), &agent.Session{ID: "s1"}), p)
}

func runSpawn(t *testing.T, tool *builtin.SpawnSubagentTool, ctx context.Context, input string) limitsOutcome {
	t.Helper()
	out, err := tool.Execute(ctx, json.RawMessage(input))
	require.NoError(t, err)
	var parsed limitsOutcome
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	return parsed
}

// schemaInt reads an integer bound out of the tool's own InputSchema so
// the tests track the advertised contract rather than a copied number.
func schemaInt(t *testing.T, tool *builtin.SpawnSubagentTool, key string, path ...string) int {
	t.Helper()
	node := tool.InputSchema()
	for _, p := range path {
		props, ok := node["properties"].(map[string]any)
		if !ok {
			if items, ok := node["items"].(map[string]any); ok {
				props, _ = items["properties"].(map[string]any)
			}
		}
		require.NotNil(t, props, "schema: no properties at %q", p)
		node, ok = props[p].(map[string]any)
		require.True(t, ok, "schema: missing %q", p)
	}
	v, ok := node[key].(int)
	require.True(t, ok, "schema: %v has no int %q", path, key)
	return v
}

func promptTasks(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"prompt":"t%d"}`, i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// overBudget counts tasks refused by the budget. Goroutine start order
// is not task order, so tests count rather than index.
func overBudget(o limitsOutcome) int {
	n := 0
	for _, task := range o.Tasks {
		if strings.Contains(task.Error, "over budget") {
			n++
		}
	}
	return n
}

func TestSpawnSubagent_CannotRaiseBudget(t *testing.T) {
	t.Run("model budget above operator budget is ignored", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 1, BudgetTokens: 10})
		p := &recordingProvider{usage: agent.Usage{InputTokens: 5, OutputTokens: 3}}
		got := runSpawn(t, tool, spawnCtx(p),
			`{"tasks":`+promptTasks(3)+`,"budget_tokens":1000000}`)
		assert.Equal(t, 1, overBudget(got),
			"operator budget of 10 must stop the third task to run")
	})

	t.Run("model budget below operator budget is honoured", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 1, BudgetTokens: 1000})
		p := &recordingProvider{usage: agent.Usage{InputTokens: 5, OutputTokens: 3}}
		got := runSpawn(t, tool, spawnCtx(p),
			`{"tasks":`+promptTasks(3)+`,"budget_tokens":10}`)
		assert.Equal(t, 1, overBudget(got))
	})

	t.Run("unlimited operator budget caps model input", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 1})
		ceiling := schemaInt(t, tool, "maximum", "budget_tokens")
		p := &recordingProvider{usage: agent.Usage{InputTokens: ceiling}}
		got := runSpawn(t, tool, spawnCtx(p),
			fmt.Sprintf(`{"tasks":%s,"budget_tokens":%d}`, promptTasks(2), ceiling*1000))
		assert.Equal(t, 1, overBudget(got),
			"model budget must be capped at the schema maximum when the operator sets none")
	})
}

func TestSpawnSubagent_CannotRaiseConcurrency(t *testing.T) {
	cases := []struct {
		name     string
		operator int
		model    int
		tasks    int
		want     int
	}{
		{"explicit operator limit", 1, 8, 4, 1},
		{"operator default of 4", 0, 16, 8, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: tc.operator})
			p := &recordingProvider{hold: 40 * time.Millisecond}
			runSpawn(t, tool, spawnCtx(p),
				fmt.Sprintf(`{"tasks":%s,"max_concurrent":%d}`, promptTasks(tc.tasks), tc.model))
			assert.LessOrEqual(t, p.maxInflight, tc.want,
				"model max_concurrent=%d must not raise operator limit", tc.model)
		})
	}
}

func TestSpawnSubagent_TaskCountEnforced(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
	limit := schemaInt(t, tool, "maxItems", "tasks")

	p := &recordingProvider{}
	_, err := tool.Execute(spawnCtx(p), json.RawMessage(`{"tasks":`+promptTasks(limit+1)+`}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("at most %d tasks", limit))
	assert.Zero(t, p.calls, "no sub-agent may run when the task count is rejected")

	got := runSpawn(t, tool, spawnCtx(&recordingProvider{}), `{"tasks":`+promptTasks(limit)+`}`)
	assert.Len(t, got.Tasks, limit)
}

func TestSpawnSubagent_TurnsAndTimeoutClamped(t *testing.T) {
	t.Run("max_turns", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{})
		limit := schemaInt(t, tool, "maximum", "tasks", "max_turns")
		p := &recordingProvider{stop: agent.StopToolUse}
		got := runSpawn(t, tool, spawnCtx(p), `{"tasks":[{"prompt":"x","max_turns":1000}]}`)
		require.Len(t, got.Tasks, 1)
		assert.Equal(t, limit, got.Tasks[0].Turns)
	})

	t.Run("timeout_seconds clamped to schema maximum", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{PerTaskTimeout: 100 * time.Hour})
		limit := schemaInt(t, tool, "maximum", "tasks", "timeout_seconds")
		p := &recordingProvider{}
		runSpawn(t, tool, spawnCtx(p), `{"tasks":[{"prompt":"x","timeout_seconds":100000000}]}`)
		require.Len(t, p.remaining, 1)
		assert.LessOrEqual(t, p.remaining[0], time.Duration(limit)*time.Second)
	})

	t.Run("timeout_seconds cannot exceed operator per-task timeout", func(t *testing.T) {
		tool := builtin.NewSpawnSubagentTool(subagent.Policy{PerTaskTimeout: time.Minute})
		p := &recordingProvider{}
		runSpawn(t, tool, spawnCtx(p), `{"tasks":[{"prompt":"x","timeout_seconds":3600}]}`)
		require.Len(t, p.remaining, 1)
		assert.LessOrEqual(t, p.remaining[0], time.Minute)
	})
}

func TestSpawnSubagent_SystemAppendsToParent(t *testing.T) {
	tool := builtin.NewSpawnSubagentTool(subagent.Policy{MaxConcurrent: 1})
	p := &recordingProvider{}
	ctx := toolcontext.WithSystemPrompt(spawnCtx(p), "PARENT RULES")

	runSpawn(t, tool, ctx, `{"tasks":[
		{"prompt":"a"},
		{"prompt":"b","system":"Focus on tests."}
	]}`)

	require.Len(t, p.systems, 2)
	assert.ElementsMatch(t,
		[]string{"PARENT RULES", "PARENT RULES\n\nFocus on tests."},
		p.systems,
		"a task's system text is appended to the parent prompt, never a replacement")
}
