// Package hooks lets operators plug external scripts into the agent
// lifecycle without recompiling. Every registered hook receives a
// JSON payload on stdin and returns a JSON verdict on stdout that the
// agent loop respects (allow / deny / modify).
//
// Motivating use cases:
//
//   - credential-scanner PreToolUse hook — grep the bash command's
//     args for aws_secret_access_key et al; return deny when found
//   - policy-as-code PostTurn hook — write a compliance-audit line to
//     a durable log
//   - cost-cap PreTurn hook — check the daemon's total spend for the
//     day against a budget; deny when over
//
// Non-goals: hooks are not a replacement for tests, code review, or
// prompt engineering. They are a coarse control that fires
// synchronously in the request path — keep them fast (default 5s
// timeout).
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Event names the lifecycle point at which a hook fires. Values
// intentionally match the surface Claude Code documents so operator
// scripts written for one agent port to the other with minimal
// changes.
type Event string

// Lifecycle event constants.
const (
	// EventPreToolUse fires immediately before a tool is executed.
	// Verdict Deny short-circuits the tool call; the model receives
	// a synthetic error result.
	EventPreToolUse Event = "pre_tool_use"
	// EventPostToolUse fires immediately after a tool completes.
	// Verdict is advisory — the tool has already run.
	EventPostToolUse Event = "post_tool_use"
	// EventPreTurn fires at the start of each provider round-trip
	// inside Agent.Turn.
	EventPreTurn Event = "pre_turn"
	// EventPostTurn fires at the end of each provider round-trip.
	EventPostTurn Event = "post_turn"
	// EventOnError fires when the agent loop encounters an error.
	EventOnError Event = "on_error"
)

// Decision is the verdict a hook returns to the agent loop.
type Decision string

// Decision constants.
const (
	// DecisionAllow — proceed with the operation.
	DecisionAllow Decision = "allow"
	// DecisionDeny — abort the operation. The Reason is surfaced to
	// the caller so the model sees why the tool was blocked.
	DecisionDeny Decision = "deny"
	// DecisionModify — proceed but with a modified payload
	// (implementation detail per event: e.g. the hook returns a
	// rewritten bash command). Modification support is per-event;
	// unsupported events treat Modify as Allow.
	DecisionModify Decision = "modify"
)

// Verdict is the parsed JSON reply a hook writes to stdout.
type Verdict struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	// Modified is an optional replacement payload for events that
	// support DecisionModify. Content is event-specific — e.g. for
	// pre_tool_use, an object with the same shape as the original
	// tool input.
	Modified json.RawMessage `json:"modified,omitempty"`
}

// Config describes one hook attached to a specific event.
type Config struct {
	// Name is a human-readable identifier surfaced in metrics + logs.
	Name string
	// Command is the executable path (resolved via $PATH).
	Command string
	// Args are the command-line arguments.
	Args []string
	// Env are extra environment variables layered on top of the
	// daemon's environment.
	Env map[string]string
	// Timeout bounds a single invocation. Zero uses 5 seconds.
	// Values above 30 seconds are permitted but discouraged — hooks
	// fire synchronously in the request path.
	Timeout time.Duration
}

// Runner runs the hooks registered for a given event and returns the
// effective verdict. Implementations are safe for concurrent use.
type Runner interface {
	// Run invokes every hook registered for event with payload as
	// stdin. Returns the FIRST non-allow verdict; if every hook
	// returns Allow, returns DecisionAllow.
	Run(ctx context.Context, event Event, payload []byte) (Verdict, error)
}

// Set is the default [Runner] implementation. It holds a per-event
// list of hooks and evaluates them in declaration order.
type Set struct {
	logger *slog.Logger
	byEvent map[Event][]Config
}

// New constructs a Set from a per-event list of hook configs. A nil
// or empty map produces a Runner that always returns DecisionAllow —
// useful as the zero-value default when no hooks are configured.
func New(byEvent map[Event][]Config, logger *slog.Logger) *Set {
	if logger == nil {
		logger = slog.Default()
	}
	out := make(map[Event][]Config, len(byEvent))
	for e, cfgs := range byEvent {
		if len(cfgs) == 0 {
			continue
		}
		out[e] = append([]Config(nil), cfgs...)
	}
	return &Set{logger: logger, byEvent: out}
}

// Run satisfies [Runner]. Returns the first non-allow verdict; when
// every hook returns Allow (or there are no hooks for event), returns
// DecisionAllow. Errors from a single hook (subprocess failed,
// verdict didn't parse) are logged at Warn and treated as Allow —
// hook failures must not be a denial-of-service on the daemon.
func (s *Set) Run(ctx context.Context, event Event, payload []byte) (Verdict, error) {
	cfgs := s.byEvent[event]
	if len(cfgs) == 0 {
		return Verdict{Decision: DecisionAllow}, nil
	}
	for _, cfg := range cfgs {
		verdict, err := runOne(ctx, cfg, payload, s.logger)
		if err != nil {
			// Fail-open: log and continue. Alternative is fail-closed,
			// but a broken hook script must not be able to lock a
			// production daemon out of tool use.
			s.logger.Warn("hook.error",
				slog.String("event", string(event)),
				slog.String("hook", cfg.Name),
				slog.String("err", err.Error()),
			)
			continue
		}
		if verdict.Decision == "" {
			verdict.Decision = DecisionAllow
		}
		if verdict.Decision != DecisionAllow {
			// First non-allow wins.
			return verdict, nil
		}
	}
	return Verdict{Decision: DecisionAllow}, nil
}

// runOne invokes a single hook script and parses its verdict.
func runOne(ctx context.Context, cfg Config, payload []byte, logger *slog.Logger) (Verdict, error) {
	if cfg.Command == "" {
		return Verdict{}, errors.New("hook: empty Command")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- operator-supplied Command, same trust boundary
	// as any subprocess in the tool registry.
	cmd := exec.CommandContext(callCtx, cfg.Command, cfg.Args...)
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)
	cmd.Stdin = bytes.NewReader(payload)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if callCtx.Err() == context.DeadlineExceeded {
		return Verdict{}, fmt.Errorf("hook %s: timed out after %s", cfg.Name, timeout)
	}
	if err != nil {
		return Verdict{}, fmt.Errorf("hook %s: exec: %w: stderr=%s", cfg.Name, err, trimForLog(stderr.String()))
	}

	logger.Debug("hook.completed",
		slog.String("hook", cfg.Name),
		slog.Duration("elapsed", elapsed),
	)

	var verdict Verdict
	stdoutStr := strings.TrimSpace(stdout.String())
	if stdoutStr == "" {
		// Empty output is treated as Allow (script that exits 0
		// without saying anything means "no objection").
		return Verdict{Decision: DecisionAllow}, nil
	}
	if err := json.Unmarshal([]byte(stdoutStr), &verdict); err != nil {
		return Verdict{}, fmt.Errorf("hook %s: parse verdict: %w: stdout=%s", cfg.Name, err, trimForLog(stdoutStr))
	}
	return verdict, nil
}

// mergeEnv layers overrides on top of base. Same semantics as
// internal/mcp/client mergeEnv (kept private to each package to
// avoid a shared-utils grab bag). Setting an override to "" unsets
// the variable in the merged list.
func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	unset := make(map[string]bool)
	for k, v := range overrides {
		if v == "" {
			unset[k] = true
		}
	}
	for _, kv := range base {
		if k, _, ok := splitEnv(kv); ok {
			if _, replace := overrides[k]; replace {
				continue
			}
			if unset[k] {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if unset[k] {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func splitEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

func trimForLog(s string) string {
	const maxLogBytes = 200
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > maxLogBytes {
		return s[:maxLogBytes] + "…"
	}
	return s
}

// -- payload helpers ---------------------------------------------------

// PreToolUsePayload is the JSON shape a PreToolUse hook sees on stdin.
type PreToolUsePayload struct {
	Event     Event           `json:"event"`
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
}

// MarshalPreToolUse builds the payload for a PreToolUse hook call.
func MarshalPreToolUse(sessionID, toolName string, input json.RawMessage) ([]byte, error) {
	return json.Marshal(PreToolUsePayload{
		Event:     EventPreToolUse,
		SessionID: sessionID,
		ToolName:  toolName,
		Input:     input,
	})
}
