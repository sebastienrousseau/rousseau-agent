package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// runTools executes every tool_use block in m and returns one
// tool_result per block, in order. It never returns a partial set: on
// cancellation the remaining blocks get a synthetic cancelled result
// so the caller can append them and the session stays a well-formed
// tool_use/tool_result sequence (a provider rejects the next turn
// otherwise). The error is still returned so the turn aborts.
func (a *Agent) runTools(ctx context.Context, m Message, sessionID string) ([]Content, error) {
	uses := toolUses(m)
	results := make([]Content, 0, len(uses))
	for i, use := range uses {
		// Between tool calls is the other safe point for pause/cancel.
		// Steered text is deliberately NOT drained here: the message
		// list must stay a well-formed tool_use/tool_result pair, so
		// injections wait for the iteration boundary.
		if err := gate(ctx); err != nil {
			return append(results, cancelledResults(uses[i:], err)...), err
		}
		results = append(results, a.runOneTool(ctx, use, sessionID))
	}
	return results, nil
}

// defaultToolTimeout bounds one tool execution when Options.ToolTimeout
// is zero.
const defaultToolTimeout = 10 * time.Minute

// defaultMaxToolOutputBytes caps tool output handed to the model when
// Options.MaxToolOutputBytes is zero.
const defaultMaxToolOutputBytes = 64 << 10

// boundOutput cuts tool output at the configured byte cap, on a UTF-8
// boundary, with a marker telling the model how much it did not see.
func (a *Agent) boundOutput(out string) string {
	limit := a.opts.MaxToolOutputBytes
	if limit <= 0 {
		limit = defaultMaxToolOutputBytes
	}
	if len(out) <= limit {
		return out
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", cut, len(out))
}

func (a *Agent) toolTimeout() time.Duration {
	if a.opts.ToolTimeout > 0 {
		return a.opts.ToolTimeout
	}
	return defaultToolTimeout
}

func toolUses(m Message) []*ToolUse {
	uses := make([]*ToolUse, 0, len(m.Content))
	for _, c := range m.Content {
		if c.Kind == ContentToolUse && c.ToolUse != nil {
			uses = append(uses, c.ToolUse)
		}
	}
	return uses
}

func cancelledResults(uses []*ToolUse, err error) []Content {
	out := make([]Content, 0, len(uses))
	for _, use := range uses {
		out = append(out, errorResult(use, "tool call cancelled before it ran: "+err.Error()))
	}
	return out
}

func errorResult(use *ToolUse, text string) Content {
	return Content{Kind: ContentToolResult, ToolResult: &ToolResult{
		ToolUseID: use.ID,
		Output:    text,
		IsError:   true,
	}}
}

// runOneTool resolves, approves, hooks and executes a single call.
// Every exit produces exactly one tool_result for use.ID.
func (a *Agent) runOneTool(ctx context.Context, use *ToolUse, sessionID string) Content {
	tool, ok := a.registry.Get(use.Name)
	if !ok {
		// A hallucinated tool name is the model's most common
		// mistake; feed it back as an error result (like a denial)
		// instead of aborting the whole turn.
		observability.ToolCalls.WithLabelValues(use.Name, "not_found").Inc()
		a.logger.Warn("tool.not_found", slog.String("name", use.Name))
		a.emitEvent(ctx, progress.Event{Kind: progress.KindToolDenied, Tool: use.Name, Err: ErrToolNotFound.Error()})
		return errorResult(use, fmt.Sprintf("%s: %s is not an available tool; use one of %s",
			ErrToolNotFound, use.Name, strings.Join(a.registry.Names(), ", ")))
	}
	// One canonical byte form for the approver, the hooks and the
	// tool: a policy cannot be dodged with a JSON escape or a repeated
	// key that the tool would decode differently from the matcher.
	canonical, err := CanonicalInput(use.Input)
	if err != nil {
		return a.denyTool(ctx, use, sessionID, ErrNonCanonicalInput.Error(), "invalid_input", "invalid_input", "tool.invalid_input", "tool call blocked: ")
	}
	use.Input = canonical
	if decision, reason := a.opts.Approver.Approve(ctx, ApprovalRequest{
		ToolName:  use.Name,
		Input:     use.Input,
		SessionID: sessionID,
	}); decision == DecisionDeny {
		if reason == "" {
			reason = "denied by policy"
		}
		return a.denyTool(ctx, use, sessionID, reason, "deny", "denied", "tool.denied", "tool call blocked: ")
	}
	if res, denied := a.preToolHook(ctx, use, sessionID); denied {
		return res
	}
	return a.executeTool(ctx, tool, use, sessionID)
}

// denyTool records a denial (metric, log, progress, audit) and
// returns the error tool_result the model sees.
func (a *Agent) denyTool(ctx context.Context, use *ToolUse, sessionID, reason, metric, auditResult, logEvent, prefix string) Content {
	observability.ToolCalls.WithLabelValues(use.Name, metric).Inc()
	a.logger.Warn(logEvent, slog.String("name", use.Name), slog.String("reason", reason))
	a.emitEvent(ctx, progress.Event{Kind: progress.KindToolDenied, Tool: use.Name, Err: reason})
	a.emitToolAudit(ctx, "deny", use.Name, auditResult, sessionID, map[string]any{"reason": reason})
	return errorResult(use, prefix+reason)
}

// preToolHook fires the PreToolUse hook AFTER the Approver so
// operators can layer policy-as-code on top of the pattern-based
// allow list. A deny returns (result, true); a modify is canonicalised
// and approved again before it replaces use.Input (see
// applyHookModify); hook failures follow each hook's fail_closed.
func (a *Agent) preToolHook(ctx context.Context, use *ToolUse, sessionID string) (Content, bool) {
	if a.opts.Hooks == nil {
		return Content{}, false
	}
	payload, mErr := hooks.MarshalPreToolUse(sessionID, use.Name, use.Input)
	if mErr != nil {
		// Should be impossible for well-formed input, but fail open
		// (log) rather than block the whole loop.
		a.logger.Warn("hook.marshal_failed", slog.String("event", string(hooks.EventPreToolUse)), slog.String("err", mErr.Error()))
		return Content{}, false
	}
	verdict, hErr := a.opts.Hooks.Run(ctx, hooks.EventPreToolUse, payload)
	if hErr != nil {
		a.logger.Warn("hook.run_failed", slog.String("event", string(hooks.EventPreToolUse)), slog.String("err", hErr.Error()))
	}
	switch verdict.Decision {
	case hooks.DecisionDeny:
		reason := verdict.Reason
		if reason == "" {
			reason = "denied by hook"
		}
		return a.denyTool(ctx, use, sessionID, reason, "hook_deny", "hook_denied", "tool.hook_denied", "tool call blocked by hook: "), true
	case hooks.DecisionModify:
		return a.applyHookModify(ctx, use, sessionID, verdict.Modified)
	}
	return Content{}, false
}

// applyHookModify replaces use.Input with a hook's modified input only
// after the Approver has approved that input too: the first approval
// covered the original, not what the hook wrote. The modified input
// is canonicalised first (same form as runOneTool gives the
// approver); one that is not valid JSON or repeats a key is denied,
// since the hook did not accept the original either. A modify that
// canonicalises to the already-approved input needs no second look.
func (a *Agent) applyHookModify(ctx context.Context, use *ToolUse, sessionID string, modified json.RawMessage) (Content, bool) {
	if len(modified) == 0 {
		return Content{}, false
	}
	canonical, err := CanonicalInput(modified)
	if err != nil {
		return a.denyTool(ctx, use, sessionID, "modified input is not valid JSON or repeats a key",
			"hook_deny", "hook_denied", "tool.hook_denied", "tool call blocked by hook: "), true
	}
	if bytes.Equal(canonical, use.Input) {
		return Content{}, false
	}
	original, rewritten := sha256Hex(use.Input), sha256Hex(canonical)
	if decision, reason := a.opts.Approver.Approve(ctx, ApprovalRequest{
		ToolName:  use.Name,
		Input:     canonical,
		SessionID: sessionID,
	}); decision == DecisionDeny {
		return a.denyModified(ctx, use, sessionID, reason, map[string]any{
			"input_sha256_original": original,
			"input_sha256_modified": rewritten,
		}), true
	}
	use.Input = canonical
	a.logger.Info("tool.hook_modified", slog.String("name", use.Name),
		slog.String("input_sha256_original", original),
		slog.String("input_sha256_modified", rewritten))
	return Content{}, false
}

// denyModified records the Approver's denial of hook-modified input,
// with the digests of both inputs on the audit record.
func (a *Agent) denyModified(ctx context.Context, use *ToolUse, sessionID, reason string, detail map[string]any) Content {
	if reason == "" {
		reason = "denied by policy"
	}
	observability.ToolCalls.WithLabelValues(use.Name, "deny").Inc()
	a.logger.Warn("tool.denied", slog.String("name", use.Name), slog.String("reason", reason), slog.String("stage", "hook_modified"))
	a.emitEvent(ctx, progress.Event{Kind: progress.KindToolDenied, Tool: use.Name, Err: reason})
	detail["reason"] = reason
	detail["stage"] = "hook_modified"
	a.emitToolAudit(ctx, "deny", use.Name, "denied", sessionID, detail)
	return errorResult(use, "tool call blocked: "+reason)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// executeTool runs an approved call under its own span and reports
// the outcome through progress, audit and metrics.
func (a *Agent) executeTool(ctx context.Context, tool tools.Tool, use *ToolUse, sessionID string) Content {
	observability.ToolCalls.WithLabelValues(use.Name, "allow").Inc()
	a.logger.Info("tool.execute", slog.String("name", use.Name), slog.String("id", use.ID))
	detail := summarizeToolInput(use.Name, use.Input)
	a.emitEvent(ctx, progress.Event{Kind: progress.KindToolStarted, Tool: use.Name, Detail: detail})
	toolStart := time.Now()
	toolCtx, toolSpan := observability.StartSpan(ctx, "agent.tool",
		attribute.String("tool.name", use.Name),
		attribute.String("tool.use_id", use.ID))
	toolCtx, cancel := context.WithTimeout(toolCtx, a.toolTimeout())
	out, err := tool.Execute(toolCtx, use.Input)
	cancel()
	if err != nil {
		if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("%w after %s: %w", ErrToolTimeout, a.toolTimeout(), err)
		}
		toolSpan.RecordError(err)
		toolSpan.SetStatus(codes.Error, err.Error())
	}
	toolSpan.End()

	done := progress.Event{Kind: progress.KindToolFinished, Tool: use.Name, Detail: detail, Elapsed: time.Since(toolStart)}
	result := &ToolResult{ToolUseID: use.ID, Output: a.boundOutput(out)}
	auditResult := "success"
	auditDetail := map[string]any{"elapsed_ms": time.Since(toolStart).Milliseconds()}
	if err != nil {
		result.IsError = true
		result.Output = a.boundOutput(err.Error())
		// done reaches chat via the explain trail; the error stays in
		// the log under the same ref.
		done.Ref = progress.NewRef()
		done.Err = progress.FailureText(done.Ref)
		a.logger.Warn("tool.error", slog.String("name", use.Name), slog.String("ref", done.Ref), slog.String("err", err.Error()))
		auditResult = "error"
		auditDetail["error"] = err.Error()
	}
	a.emitEvent(ctx, done)
	a.emitToolAudit(ctx, "run", use.Name, auditResult, sessionID, auditDetail)
	return Content{Kind: ContentToolResult, ToolResult: result}
}
