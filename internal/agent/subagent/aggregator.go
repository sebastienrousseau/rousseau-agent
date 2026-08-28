package subagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Aggregator combines a slice of Results into a single Content block
// suitable for appending to the parent session as if it were a
// tool_result. Callers can implement their own for bespoke merging;
// [DefaultAggregator] handles the common case.
type Aggregator interface {
	Aggregate(results []Result, maxBytes int) agent.Content
}

// DefaultAggregator emits a numbered list of per-task summaries
// (task index, duration, tokens, final text, error). Total output is
// bounded to maxBytes; earlier tasks are preferred so their
// summaries survive truncation on a large-result run.
type DefaultAggregator struct{}

// Aggregate implements Aggregator.
func (DefaultAggregator) Aggregate(results []Result, maxBytes int) agent.Content {
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	var b strings.Builder
	toolUseID := "spawn-" + uuid.NewString()

	for i, r := range results {
		block := formatOne(i, r)
		if b.Len()+len(block) > maxBytes {
			remaining := maxBytes - b.Len()
			if remaining > 32 {
				b.WriteString(block[:remaining-32])
			}
			fmt.Fprintf(&b, "\n[truncated %d more task(s)]", len(results)-i)
			break
		}
		b.WriteString(block)
	}

	return agent.Content{
		Kind: agent.ContentToolResult,
		ToolResult: &agent.ToolResult{
			ToolUseID: toolUseID,
			Output:    b.String(),
		},
	}
}

// AggregateJSON is an alternative aggregator that renders the results
// as compact JSON. Useful when the calling model has a JSON-schema
// expectation for the spawn tool.
func AggregateJSON(results []Result, maxBytes int) agent.Content {
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	// Shape the slice as a []map[string]any so json.Marshal produces
	// the shortest possible key set (skip zero fields).
	rows := make([]map[string]any, 0, len(results))
	for _, r := range results {
		row := map[string]any{
			"task_index":  r.TaskIndex,
			"turns":       r.Turns,
			"tokens_in":   r.TokensIn,
			"tokens_out":  r.TokensOut,
			"duration_ms": r.Duration.Milliseconds(),
		}
		if r.FinalText != "" {
			row["final_text"] = r.FinalText
		}
		if r.Err != nil {
			row["error"] = r.Err.Error()
		}
		rows = append(rows, row)
	}
	raw, _ := json.Marshal(rows) //nolint:errcheck // maps of primitives always marshal
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
	}
	return agent.Content{
		Kind: agent.ContentToolResult,
		ToolResult: &agent.ToolResult{
			ToolUseID: "spawn-" + uuid.NewString(),
			Output:    string(raw),
		},
	}
}

// formatOne renders one result as a human-readable block. Kept short
// so the aggregate stays within reasonable token counts.
func formatOne(i int, r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "── task %d ", i)
	if r.Err != nil {
		fmt.Fprintf(&b, "(ERROR: %s) ", r.Err.Error())
	}
	fmt.Fprintf(&b, "· %d turn(s) · %d↓/%d↑ tokens · %dms\n",
		r.Turns, r.TokensIn, r.TokensOut, r.Duration.Milliseconds())
	if r.FinalText != "" {
		fmt.Fprintf(&b, "%s\n", r.FinalText)
	}
	b.WriteString("\n")
	return b.String()
}
