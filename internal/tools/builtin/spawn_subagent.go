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
	// DefaultPolicy is the operator's envelope. The model may only
	// tighten it: a budget_tokens / max_concurrent / timeout_seconds
	// above the operator value is cut to it. Zero-value uses
	// [subagent.Policy]'s own defaults (MaxConcurrent=4,
	// PerTaskTimeout=5min, no budget).
	DefaultPolicy subagent.Policy
}

// Server-side bounds on model input. InputSchema advertises the same
// numbers, but a schema is advice to the model, not enforcement.
const (
	maxSpawnTasks         = 16
	maxTaskTurns          = 16
	maxTaskTimeoutSeconds = 3600
	maxSpawnConcurrent    = 16
	// maxModelBudgetTokens caps a model-supplied budget_tokens when the
	// operator set no budget (unlimited). Any finite value already
	// lowers "unlimited"; the cap only keeps the number sane: 2M tokens
	// covers maxSpawnTasks x maxTaskTurns round-trips at ~8k tokens.
	maxModelBudgetTokens = 2_000_000
)

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
				"maxItems":    maxSpawnTasks,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"description": "The user-turn text handed to the sub-agent. Required.",
						},
						"system": map[string]any{
							"type":        "string",
							"description": "Optional extra system-prompt text, appended to the parent's system prompt. Empty inherits the parent's prompt unchanged.",
						},
						"max_turns": map[string]any{
							"type":        "integer",
							"description": "Cap on model round-trips within the sub-agent. Zero uses 4.",
							"minimum":     0,
							"maximum":     maxTaskTurns,
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Wall-clock cap for this task. Zero uses the parent's per-task timeout, which is also the ceiling.",
							"minimum":     0,
							"maximum":     maxTaskTimeoutSeconds,
						},
					},
					"required": []string{"prompt"},
				},
			},
			"budget_tokens": map[string]any{
				"type":        "integer",
				"description": "Total (input+output) token ceiling across every task. Zero uses the tool's configured default (or no cap). Can only lower the configured budget.",
				"minimum":     0,
				"maximum":     maxModelBudgetTokens,
			},
			"max_concurrent": map[string]any{
				"type":        "integer",
				"description": "How many sub-agents may run at once. Zero uses the tool's configured default (typically 4). Can only lower the configured limit.",
				"minimum":     0,
				"maximum":     maxSpawnConcurrent,
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
	Total      int `json:"total"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	TokensIn   int `json:"tokens_in"`
	TokensOut  int `json:"tokens_out"`
	DurationMs int `json:"duration_ms"`
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
	in, err := parseSpawnInput(raw)
	if err != nil {
		return "", err
	}
	session, provider, err := spawnDeps(ctx)
	if err != nil {
		return "", err
	}
	logger := toolcontext.Logger(ctx)

	policy := t.effectivePolicy(in)
	tasks := buildTasks(in.Tasks, toolcontext.SystemPrompt(ctx), policy.EffectivePerTaskTimeout())

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

	out := summarise(results, elapsed)
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

// parseSpawnInput decodes the input and rejects what the schema
// forbids but a model can still send: no tasks, too many tasks, or a
// task without a prompt.
func parseSpawnInput(raw json.RawMessage) (spawnInput, error) {
	var in spawnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("spawn_subagent: parse input: %w", err)
	}
	if len(in.Tasks) == 0 {
		return in, fmt.Errorf("spawn_subagent: at least one task required")
	}
	if len(in.Tasks) > maxSpawnTasks {
		return in, fmt.Errorf("spawn_subagent: at most %d tasks allowed, got %d", maxSpawnTasks, len(in.Tasks))
	}
	for i, spec := range in.Tasks {
		if spec.Prompt == "" {
			return in, fmt.Errorf("spawn_subagent: task %d has empty prompt", i)
		}
	}
	return in, nil
}

// spawnDeps pulls the parent session and provider off ctx.
func spawnDeps(ctx context.Context) (*agent.Session, agent.Provider, error) {
	sessionRaw, ok := toolcontext.Session(ctx)
	if !ok {
		return nil, nil, ErrSpawnMissingContext
	}
	session, ok := sessionRaw.(*agent.Session)
	if !ok {
		return nil, nil, fmt.Errorf("spawn_subagent: session context value has type %T, want *agent.Session", sessionRaw)
	}
	providerRaw, ok := toolcontext.Provider(ctx)
	if !ok {
		return nil, nil, ErrSpawnMissingContext
	}
	provider, ok := providerRaw.(agent.Provider)
	if !ok {
		return nil, nil, fmt.Errorf("spawn_subagent: provider context value has type %T, want agent.Provider", providerRaw)
	}
	return session, provider, nil
}

// effectivePolicy merges the model's requested limits into the
// operator's policy. The model can only lower a limit, never raise it.
func (t *SpawnSubagentTool) effectivePolicy(in spawnInput) subagent.Policy {
	policy := t.DefaultPolicy
	policy.BudgetTokens = lowerOnly(in.BudgetTokens, policy.BudgetTokens, maxModelBudgetTokens)
	policy.MaxConcurrent = lowerOnly(in.MaxConcurrent, policy.EffectiveMaxConcurrent(), maxSpawnConcurrent)
	return policy
}

// lowerOnly returns the limit to enforce given a model request, the
// operator's limit (<= 0 means unlimited) and a hard ceiling on model
// input. A non-positive request keeps the operator limit.
func lowerOnly(requested, operator, ceiling int) int {
	if requested <= 0 {
		return operator
	}
	requested = min(requested, ceiling)
	if operator > 0 {
		return min(requested, operator)
	}
	return requested
}

// buildTasks converts the model's task specs into subagent.Tasks with
// turns and timeout clamped and the parent's system prompt inherited.
func buildTasks(specs []spawnTaskInput, parentSystem string, operatorTimeout time.Duration) []subagent.Task {
	tasks := make([]subagent.Task, len(specs))
	for i, spec := range specs {
		tasks[i] = subagent.Task{
			Prompt:   spec.Prompt,
			System:   joinSystem(parentSystem, spec.System),
			MaxTurns: max(0, min(spec.MaxTurns, maxTaskTurns)),
			Timeout:  taskTimeout(spec.TimeoutSeconds, operatorTimeout),
		}
	}
	return tasks
}

// taskTimeout clamps a model-requested timeout to the schema maximum
// and the operator's per-task timeout. Zero (or negative) defers to
// the policy default.
func taskTimeout(seconds int, operator time.Duration) time.Duration {
	if seconds <= 0 {
		return 0
	}
	d := time.Duration(min(seconds, maxTaskTimeoutSeconds)) * time.Second
	return min(d, operator)
}

// joinSystem appends a task's system text to the parent's prompt. The
// task can add instructions but never drop the parent's.
func joinSystem(parent, extra string) string {
	switch {
	case extra == "":
		return parent
	case parent == "":
		return extra
	default:
		return parent + "\n\n" + extra
	}
}

// summarise folds per-task results into the model-facing output.
func summarise(results []subagent.Result, elapsed time.Duration) spawnOutput {
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
	return out
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*SpawnSubagentTool)(nil)
