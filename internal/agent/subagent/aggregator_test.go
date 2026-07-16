package subagent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestDefaultAggregator_IncludesEveryTask(t *testing.T) {
	agg := DefaultAggregator{}
	results := []Result{
		{TaskIndex: 0, FinalText: "first answer", Turns: 1, Duration: 100 * time.Millisecond},
		{TaskIndex: 1, FinalText: "second answer", Turns: 2, Duration: 200 * time.Millisecond},
	}
	out := agg.Aggregate(results, 4096)
	assert.Equal(t, agent.ContentToolResult, out.Kind)
	require.NotNil(t, out.ToolResult)
	assert.Contains(t, out.ToolResult.Output, "first answer")
	assert.Contains(t, out.ToolResult.Output, "second answer")
	assert.Contains(t, out.ToolResult.Output, "task 0")
	assert.Contains(t, out.ToolResult.Output, "task 1")
}

func TestDefaultAggregator_IncludesErrors(t *testing.T) {
	agg := DefaultAggregator{}
	out := agg.Aggregate([]Result{
		{TaskIndex: 0, Err: errors.New("upstream fail")},
	}, 4096)
	assert.Contains(t, out.ToolResult.Output, "ERROR: upstream fail")
}

func TestDefaultAggregator_TruncatesLargeInputs(t *testing.T) {
	agg := DefaultAggregator{}
	big := strings.Repeat("x", 500)
	results := []Result{
		{TaskIndex: 0, FinalText: big, Turns: 1},
		{TaskIndex: 1, FinalText: big, Turns: 1},
		{TaskIndex: 2, FinalText: big, Turns: 1},
	}
	out := agg.Aggregate(results, 300)
	assert.LessOrEqual(t, len(out.ToolResult.Output), 300+50) // slack for truncation-notice text
	assert.Contains(t, out.ToolResult.Output, "truncated")
}

func TestDefaultAggregator_ZeroMaxBytesUsesDefault(t *testing.T) {
	agg := DefaultAggregator{}
	out := agg.Aggregate([]Result{{TaskIndex: 0, FinalText: "hi", Turns: 1}}, 0)
	assert.NotEmpty(t, out.ToolResult.Output)
}

func TestAggregateJSON_ShapesRows(t *testing.T) {
	results := []Result{
		{TaskIndex: 0, FinalText: "answer", Turns: 2, TokensIn: 10, TokensOut: 4, Duration: 500 * time.Millisecond},
		{TaskIndex: 1, Err: errors.New("boom")},
	}
	out := AggregateJSON(results, 4096)
	assert.Equal(t, agent.ContentToolResult, out.Kind)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.ToolResult.Output), &rows))
	require.Len(t, rows, 2)
	assert.EqualValues(t, 0, rows[0]["task_index"])
	assert.EqualValues(t, 500, rows[0]["duration_ms"])
	assert.EqualValues(t, "answer", rows[0]["final_text"])
	assert.EqualValues(t, "boom", rows[1]["error"])
}

func TestAggregateJSON_TruncatesOnOverflow(t *testing.T) {
	rs := make([]Result, 100)
	for i := range rs {
		rs[i] = Result{TaskIndex: i, FinalText: strings.Repeat("x", 100)}
	}
	out := AggregateJSON(rs, 200)
	assert.LessOrEqual(t, len(out.ToolResult.Output), 200)
}
