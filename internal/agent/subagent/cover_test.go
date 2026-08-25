package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestAggregateJSON_ZeroMaxBytesUsesDefault proves the 32 KiB default
// applies when the policy leaves AggregatorMaxBytes unset.
func TestAggregateJSON_ZeroMaxBytesUsesDefault(t *testing.T) {
	results := []Result{
		{TaskIndex: 0, Turns: 1, FinalText: strings.Repeat("x", 40*1024)},
	}
	c := AggregateJSON(results, 0)
	require.NotNil(t, c.ToolResult)
	assert.Len(t, c.ToolResult.Output, 32*1024)
}

func TestAggregateJSON_HonoursExplicitBudget(t *testing.T) {
	c := AggregateJSON([]Result{{TaskIndex: 0, FinalText: "hello"}}, 1<<20)
	require.NotNil(t, c.ToolResult)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(c.ToolResult.Output), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "hello", rows[0]["final_text"])
}

// TestPolicy_ExplicitValuesWin complements TestPolicy_Defaults.
func TestPolicy_ExplicitValuesWin(t *testing.T) {
	p := Policy{MaxConcurrent: 2, PerTaskTimeout: 90 * time.Second, AggregatorMaxBytes: 512}
	assert.Equal(t, 2, p.maxConcurrent())
	assert.Equal(t, 90*time.Second, p.perTaskTimeout())
	assert.Equal(t, 512, p.aggregatorMax())
}

// TestNewSemaphore_ClampsToOne proves a non-positive size still yields a
// usable, serialising gate rather than a zero-capacity deadlock.
func TestNewSemaphore_ClampsToOne(t *testing.T) {
	for _, n := range []int{0, -5} {
		s := newSemaphore(n)
		require.NoError(t, s.acquire(context.Background()))

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := s.acquire(ctx)
		cancel()
		assert.ErrorIs(t, err, context.DeadlineExceeded, "capacity must be exactly 1")

		s.release()
		assert.NoError(t, s.acquire(context.Background()))
	}
}

// scriptedProvider returns a fixed response for every Complete call.
type scriptedProvider struct {
	resp  agent.Response
	calls int
}

func (*scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	p.calls++
	return p.resp, nil
}

// TestRunOne_CancelledContextStopsBeforeProvider proves a dead context
// is detected at the top of the turn loop, so no provider call is made.
func TestRunOne_CancelledContextStopsBeforeProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &scriptedProvider{}
	res := runOne(ctx, agent.NewSession("parent"), p, Task{Prompt: "hi"})
	assert.ErrorIs(t, res.Err, context.Canceled)
	assert.Zero(t, p.calls)
	assert.Zero(t, res.Turns)
}

// TestRunOne_ExhaustsMaxTurnsWithoutEndTurn covers the loop-bound exit:
// a provider that never signals end_turn must stop at MaxTurns instead
// of spinning. The reply carries no text block, so FinalText stays empty.
func TestRunOne_ExhaustsMaxTurnsWithoutEndTurn(t *testing.T) {
	p := &scriptedProvider{resp: agent.Response{
		StopReason: agent.StopToolUse,
		Message: agent.Message{
			Role: agent.RoleAssistant,
			Content: []agent.Content{{
				Kind:    agent.ContentToolUse,
				ToolUse: &agent.ToolUse{ID: "tu-1", Name: "read"},
			}},
		},
		Usage: agent.Usage{InputTokens: 3, OutputTokens: 5},
	}}

	res := runOne(context.Background(), agent.NewSession("parent"), p, Task{Prompt: "go", MaxTurns: 3})
	assert.Nil(t, res.Err)
	assert.Equal(t, 3, p.calls)
	assert.Equal(t, 3, res.Turns)
	assert.Equal(t, 9, res.TokensIn)
	assert.Equal(t, 15, res.TokensOut)
	assert.Empty(t, res.FinalText, "a tool_use-only reply has no text to report")
}
