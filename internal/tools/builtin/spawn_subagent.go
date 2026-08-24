package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// SpawnSubagentTool exposes [subagent.Spawn] as a callable Tool so the
// parent model can dispatch parallel sub-agent work as part of its own
// tool loop. Each task runs against a detached copy of the parent
// session using the parent's Provider (both retrieved from ctx via
// [toolcontext]) with per-task and aggregate limits enforced by the
// underlying Policy.
//
// The tool is safe to omit from a build if a specific deployment
// doesn't want sub-agents — nothing else in the codebase depends on
// its registration.
type SpawnSubagentTool struct {
	// DefaultPolicy applies when the caller does not override budget /
	// concurrency / timeout. Zero-value uses [subagent.Policy]'s own
	// defaults (MaxConcurrent=4, PerTaskTimeout=5min, no budget).
	DefaultPolicy subagent.Policy
}

// NewSpawnSubagentTool constructs the tool with the given default
// policy. Pass a zero-value Policy for the built-in defaults.
func NewSpawnSubagentTool(defaultPolicy subagent.Policy) *SpawnSubagentTool {
	return &SpawnSubagentTool{DefaultPolicy: defaultPolicy}
}

// Name returns the tool identifier.
func (*SpawnSubagentTool) Name() string { return "spawn_subagent" }

// Description returns the model-facing description.
func (*SpawnSubagentTool) Description() string {
	return "Spawn one or more sub-agents in parallel against a detached copy " +
		"of the current session. Use this when a request naturally decomposes " +
		"into independent subtasks (e.g. \"review these 3 files\", " +
		"\"summarise each of these documents\", \"draft PRs for each item\"). " +
		"Each task returns its final assistant text. Prefer 2-6 tasks; more " +
		"than 8 rarely wins over sequential work."
}

// InputSchema returns the tool's input JSON Schema.
func (*SpawnSubagentTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"description": "The parallel subtasks to spawn. Each runs against a detached copy of the current session.",
				"minItems":    1,
				"maxItems":    16,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"description": "The user-turn text handed to the sub-agent. Required.",
						},
						"system": map[string]any{
							"type":        "string",
							"description": "Optional system-prompt override. Empty inherits the parent's system prompt.",
						},
						"max_turns": map[string]any{
							"type":        "integer",
							"description": "Cap on model round-trips within the sub-agent. Zero uses 4.",
							"minimum":     0,
							"maximum":     16,
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Wall-clock cap for this task. Zero uses the parent's per-task timeout.",
							"minimum":     0,
							"maximum":     3600,
						},
					},
					"required": []string{"prompt"},
				},
			},
			"budget_tokens": map[string]any{
				"type":        "integer",
				"description": "Total (input+output) token ceiling across every task. Zero uses the tool's configured default (or no cap).",
				"minimum":     0,
			},
			"max_concurrent": map[string]any{
				"type":        "integer",
				"description": "How many sub-agents may run at once. Zero uses the tool's configured default (typically 4).",
				"minimum":     0,
				"maximum":     16,
			},
		},
		"required": []string{"tasks"},
	}
}

// spawnInput is the parsed shape of the tool's input.
type spawnInput struct {
	Tasks         []spawnTaskInput `json:"tasks"`
	BudgetTokens  int              `json:"budget_tokens,omitempty"`
	MaxConcurrent int              `json:"max_concurrent,omitempty"`
}

type spawnTaskInput struct {
	Prompt         string `json:"prompt"`
	System         string `json:"system,omitempty"`
	MaxTurns       int    `json:"max_turns,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// spawnOutput is the shape returned to the model. Structured so the
// model can reason about which tasks failed vs succeeded rather than
// parsing a flat string.
type spawnOutput struct {
	Summary spawnSummary       `json:"summary"`
	Tasks   []spawnTaskOutcome `json:"tasks"`
}

type spawnSummary struct {
	Total       int `json:"total"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	TokensIn    int `json:"tokens_in"`
	TokensOut   int `json:"tokens_out"`
	DurationMs  int `json:"duration_ms"`
}

type spawnTaskOutcome struct {
	Index      int    `json:"index"`
	FinalText  string `json:"final_text,omitempty"`
	Turns      int    `json:"turns"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMs int    `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// ErrSpawnMissingContext is returned when the tool is invoked outside
// a runTools context that carries the parent session and provider —
// which should not happen inside the agent loop but is possible when a
// test invokes the tool directly.
var ErrSpawnMissingContext = errors.New("spawn_subagent: parent session and provider must be set on ctx (via toolcontext.WithSession / WithProvider)")

// Execute parses the input, runs [subagent.Spawn], and returns a JSON
// summary the model can reason about.
func (t *SpawnSubagentTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in spawnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("spawn_subagent: parse input: %w", err)
	}
	if len(in.Tasks) == 0 {
		return "", fmt.Errorf("spawn_subagent: at least one task required")
	}

	sessionRaw, ok := toolcontext.Session(ctx)
	if !ok {
		return "", ErrSpawnMissingContext
	}
	session, ok := sessionRaw.(*agent.Session)
	if !ok {
		return "", fmt.Errorf("spawn_subagent: session context value has type %T, want *agent.Session", sessionRaw)
	}
	providerRaw, ok := toolcontext.Provider(ctx)
	if !ok {
		return "", ErrSpawnMissingContext
	}
	provider, ok := providerRaw.(agent.Provider)
	if !ok {
		return "", fmt.Errorf("spawn_subagent: provider context value has type %T, want agent.Provider", providerRaw)
	}
	logger := toolcontext.Logger(ctx)

	// Build subagent.Task list.
	tasks := make([]subagent.Task, len(in.Tasks))
	for i, spec := range in.Tasks {
		if spec.Prompt == "" {
			return "", fmt.Errorf("spawn_subagent: task %d has empty prompt", i)
		}
		tasks[i] = subagent.Task{
			Prompt:   spec.Prompt,
			System:   spec.System,
			MaxTurns: spec.MaxTurns,
			Timeout:  time.Duration(spec.TimeoutSeconds) * time.Second,
		}
	}

	// Merge caller overrides with default policy.
	policy := t.DefaultPolicy
	if in.BudgetTokens > 0 {
		policy.BudgetTokens = in.BudgetTokens
	}
	if in.MaxConcurrent > 0 {
		policy.MaxConcurrent = in.MaxConcurrent
	}

	logger.Info("spawn_subagent.dispatch",
		slog.Int("tasks", len(tasks)),
		slog.Int("budget_tokens", policy.BudgetTokens),
		slog.Int("max_concurrent", policy.MaxConcurrent),
	)

	start := time.Now()
	results, err := subagent.Spawn(ctx, session, provider, tasks, policy, logger)
	elapsed := time.Since(start)
	if err != nil {
		return "", fmt.Errorf("spawn_subagent: %w", err)
	}

	out := spawnOutput{
		Summary: spawnSummary{
			Total:      len(results),
			DurationMs: int(elapsed / time.Millisecond),
		},
		Tasks: make([]spawnTaskOutcome, len(results)),
	}
	for i, r := range results {
		o := spawnTaskOutcome{
			Index:      r.TaskIndex,
			FinalText:  r.FinalText,
			Turns:      r.Turns,
			TokensIn:   r.TokensIn,
			TokensOut:  r.TokensOut,
			DurationMs: int(r.Duration / time.Millisecond),
		}
		if r.Err != nil {
			o.Error = r.Err.Error()
			out.Summary.Failed++
		} else {
			out.Summary.Succeeded++
		}
		out.Summary.TokensIn += r.TokensIn
		out.Summary.TokensOut += r.TokensOut
		out.Tasks[i] = o
	}

	logger.Info("spawn_subagent.completed",
		slog.Int("total", out.Summary.Total),
		slog.Int("succeeded", out.Summary.Succeeded),
		slog.Int("failed", out.Summary.Failed),
		slog.Int("tokens_in", out.Summary.TokensIn),
		slog.Int("tokens_out", out.Summary.TokensOut),
		slog.Duration("elapsed", elapsed),
	)

	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("spawn_subagent: marshal output: %w", err)
	}
	return string(blob), nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*SpawnSubagentTool)(nil)
