package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Options tunes the Agent loop.
type Options struct {
	// MaxIterations caps how many model round-trips a single Turn may
	// perform. Zero uses the default (32).
	MaxIterations int
	// SystemPrompt is prepended to every request.
	SystemPrompt string
	// Approver is consulted before each tool execution. Nil uses
	// AllowAllApprover — every call runs. Denials are surfaced back to
	// the model as a tool_result error so the model can pick a
	// different action.
	Approver Approver
	// Compressor is consulted at the start of each Turn. Nil uses
	// NoopCompressor. Implementations that decide to rewrite the
	// session do so in place; the loop then proceeds against the
	// smaller message list.
	Compressor Compressor
	// SkillsProvider is asked for a system-prompt appendix based on
	// the session's most recent user message. Nil disables the feature.
	SkillsProvider SkillsProvider
	// RecallProvider is asked for a system-prompt appendix drawn from
	// prior sessions. Nil disables the feature.
	RecallProvider RecallProvider
	// CostRecorder receives one entry per successful provider.Complete
	// call so cost can be aggregated per session and reported via
	// `rousseau session cost`. Nil disables cost telemetry entirely.
	CostRecorder CostRecorder
	// Progress receives live progress events for every turn this Agent
	// runs, so a chat transport can show the user what is happening
	// during a long turn. Nil disables emission.
	//
	// A per-turn publisher on the context (progress.WithPublisher, set
	// by a supervisor) takes precedence over this one — Options is
	// shared by every conversation the daemon serves, the context is
	// not.
	Progress progress.Publisher
	// Hooks is the lifecycle-hook runner. When non-nil the agent
	// loop consults it at pre_tool_use before each tool call.
	// A Deny verdict blocks the tool and surfaces the reason to the
	// model as a synthetic tool-result error. Nil disables hooks
	// entirely.
	Hooks hooks.Runner
	// AuditSink receives one [audit_egress.Record] per tool-call
	// decision (approver-deny, hook-deny, execute-success,
	// execute-error). Nil disables audit emission — Emit calls
	// simply don't happen. The daemon wires this to the shared
	// enterprise sink assembled in cli/daemon.go.
	//
	// The Actor field of every emitted record is populated from
	// [sso.IdentityFromContext] when available; anonymous
	// requests emit `actor: "anonymous"`.
	AuditSink audit_egress.Sink
	// Reliability receives per-turn samples the four-dimension
	// decomposition (arXiv:2602.16666) is built on. Turn emits:
	//
	//   - resource_cv_latency (Consistency): wall-clock ms per turn,
	//     bucketed by SessionID so per-session variance rolls up
	//     into the Consistency ring.
	//   - resource_cv_tokens (Consistency): sum of input + output
	//     tokens per turn, same bucket.
	//   - resource_cv_calls (Consistency): tool-call count per turn.
	//   - turn (Safety): 1 when the turn completed without a
	//     harmful-tool denial, 0 otherwise.
	//   - fault (Robustness): 1/0 outcome stratified by whether an
	//     upstream fault was observed.
	//   - pair (Predictability): (confidence, outcome) when
	//     EnableConfidenceElicitation is true and the model emitted
	//     a <confidence>0.XX</confidence> tag.
	//
	// Nil recorder means no telemetry (NopRecorder). Wired by the
	// daemon assembly to a [reliability.MultiRecorder] fanning to
	// the process-scoped Aggregator + the SQLite persistent store.
	Reliability reliability.Recorder
	// EnableConfidenceElicitation appends a short instruction to the
	// system prompt asking the model to close every turn with a
	// <confidence>0.XX</confidence> tag. Turn parses the value,
	// pairs it with the outcome (success = end_turn without error,
	// failure = anything else), and emits a Predictability "pair"
	// sample. The Brier score aggregator in package reliability
	// consumes these pairs.
	//
	// Off by default because it costs a few tokens per turn and
	// isn't useful without a running reliability collector. Turn
	// on when you want Predictability numbers in `rousseau
	// reliability`.
	EnableConfidenceElicitation bool
}

// CostRecorder is the seam the agent loop uses to persist per-call
// cost telemetry. Implementations must be safe for concurrent use.
// Errors returned from Record are logged at Warn but never abort the
// agent loop — cost telemetry is best-effort observability, not a
// correctness dependency.
type CostRecorder interface {
	Record(ctx context.Context, r CostEvent) error
}

// CostEvent is what the agent loop hands to a CostRecorder after
// every completion. Provider + Model may be empty for older provider
// implementations that don't populate them.
type CostEvent struct {
	SessionID string
	Provider  string
	Model     string
	Usage     Usage
}

// SkillsProvider returns text spliced into the system prompt for a
// given session. Implementations typically look at the last user
// message and select relevant skills.
type SkillsProvider interface {
	SystemAppendix(s *Session) string
}

// Agent orchestrates the model / tool-use loop against a Session.
type Agent struct {
	provider Provider
	registry *tools.Registry
	logger   *slog.Logger
	opts     Options
}

// New constructs an Agent from its collaborators.
func New(provider Provider, registry *tools.Registry, logger *slog.Logger, opts Options) *Agent {
	if opts.MaxIterations == 0 {
		opts.MaxIterations = 32
	}
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Approver == nil {
		opts.Approver = AllowAllApprover{}
	}
	if opts.Compressor == nil {
		opts.Compressor = NoopCompressor{}
	}
	return &Agent{
		provider: provider,
		registry: registry,
		logger:   logger,
		opts:     opts,
	}
}

// Turn advances the Session by one user turn: it sends the current
// message history to the model, executes any requested tools, and loops
// until the model emits an end-of-turn response. The final assistant
// Message is returned; the Session is mutated in place.
//
// Turn consults the configured Compressor before running the loop.
// Compression happens in place; long sessions keep fitting the model's
// context without the caller having to intervene.
func (a *Agent) Turn(ctx context.Context, s *Session) (Message, error) {
	start := time.Now()
	a.emit(ctx, s, progress.Event{Kind: progress.KindTurnStarted})
	stats := &turnStats{}
	msg, err := a.turnWithStats(ctx, s, stats)
	a.emitTerminal(ctx, s, start, err)
	a.recordReliabilitySamples(s, start, err, stats, msg)
	return msg, err
}

// turnStats accumulates per-turn counters the outer Turn wants to
// record after the loop returns — tokens across every provider
// round-trip, tool calls attempted (allowed OR denied), iteration
// count. Kept simple: no mutex because turn() is single-goroutine.
// Nil is treated as "don't accumulate" so callers who never look
// at stats can pass nil safely.
type turnStats struct {
	inputTokens  int
	outputTokens int
	toolCalls    int
}

// recordReliabilitySamples fires one Sample per dimension the agent
// loop can measure without instrumenting every provider / tool /
// approver call:
//
//   - Consistency: resource_cv_latency (per-turn wall-clock ms),
//     bucketed by session so cross-turn variance within a
//     conversation rolls up into the paper's C_res.
//   - Safety: turn (1 = completed cleanly, 0 = error). Violation
//     samples with severity come from the approver wrapper — this
//     signal is the "everything else went fine" turn counter.
//
// A nil Reliability recorder is a no-op via the NopRecorder
// contract; callers never need to check. Latency of the recording
// itself is a map lookup + two struct copies — cheap enough to sit
// in the hot path.
//
// Tokens + tool-call counts + confidence pairs come from follow-on
// instrumentation (provider.Complete, tool dispatch, confidence
// prompt) — this method only records what Turn can see at the
// outer bracket without touching the inner loop.
func (a *Agent) recordReliabilitySamples(s *Session, start time.Time, turnErr error, stats *turnStats, final Message) {
	rec := a.opts.Reliability
	if rec == nil {
		return
	}
	now := time.Now()
	sessID := ""
	if s != nil {
		sessID = s.ID
	}
	rec.Record(reliability.Sample{
		At:        now,
		Dimension: reliability.DimConsistency,
		SubMetric: "resource_cv_latency",
		Value:     float64(now.Sub(start).Milliseconds()),
		SessionID: sessID,
		// Bucket by session so the paper's per-bucket CV is
		// computed over "how variable is this specific
		// conversation's turn latency" — the operator-meaningful
		// question. Cross-session variance is a different metric.
		Bucket: sessID,
	})
	turnValue := 1.0
	if turnErr != nil {
		turnValue = 0
	}
	rec.Record(reliability.Sample{
		At:        now,
		Dimension: reliability.DimSafety,
		SubMetric: "turn",
		Value:     turnValue,
		SessionID: sessID,
	})
	// Robustness fault stratification: every turn contributes a
	// (success, fault_observed) pair the aggregator's
	// faultRobustness code turns into Acc_faulted / Acc_clean.
	// "Fault observed" is a heuristic — we can't distinguish a
	// provider-network-error from an agent-logic-error at this
	// layer, but string-classify the returned error so the two
	// strata at least exist when the daemon experiences real
	// upstream faults.
	rec.Record(reliability.Sample{
		At:        now,
		Dimension: reliability.DimRobustness,
		SubMetric: "fault",
		Value:     turnValue,
		SessionID: sessID,
		Metadata: map[string]string{
			"fault": faultObservedFromError(turnErr),
		},
	})
	// Consistency C_res sub-metrics: token totals + tool-call
	// count per turn, bucketed by session so cross-turn CV
	// within a conversation rolls up. Zero values still emitted
	// so the histograms have observations even on trivial turns
	// (the CV computation ignores zero-mean buckets anyway).
	if stats != nil {
		tokens := float64(stats.inputTokens + stats.outputTokens)
		rec.Record(reliability.Sample{
			At:        now,
			Dimension: reliability.DimConsistency,
			SubMetric: "resource_cv_tokens",
			Value:     tokens,
			SessionID: sessID,
			Bucket:    sessID,
		})
		rec.Record(reliability.Sample{
			At:        now,
			Dimension: reliability.DimConsistency,
			SubMetric: "resource_cv_calls",
			Value:     float64(stats.toolCalls),
			SessionID: sessID,
			Bucket:    sessID,
		})
	}
	// Predictability pair sample when confidence elicitation is on
	// AND the model actually emitted the tag. A missing tag is not
	// an error — some turns are aborted / tool-heavy and never
	// reach the closing instruction. Aggregator's Brier/ECE/AUROC
	// simply see fewer pairs.
	if a.opts.EnableConfidenceElicitation {
		if conf, ok := parseConfidence(finalText(final)); ok {
			outcome := "1"
			if turnErr != nil {
				outcome = "0"
			}
			rec.Record(reliability.Sample{
				At:        now,
				Dimension: reliability.DimPredictability,
				SubMetric: "pair",
				Value:     conf,
				SessionID: sessID,
				Metadata: map[string]string{
					"outcome": outcome,
				},
			})
		}
	}
}

// finalText concatenates the ContentText blocks of a message into
// one string so parseConfidence has a single-shot input regardless
// of how the model split its reply across content blocks.
func finalText(m Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Kind == ContentText && c.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// faultObservedFromError classifies whether the turn's terminal
// error (if any) looks like an upstream fault — provider network
// glitch, tool subprocess crash, MCP server dial failure — as
// opposed to an internal-logic failure. Returns "true" or
// "false" (string form so it round-trips through the reliability
// Sample.Metadata map cleanly).
//
// Heuristic — matches on error text. When rousseau's provider /
// tool layers get first-class error types the switch will move
// to errors.Is. For today, substring matching is sufficient and
// covers the majority of real upstream faults in practice.
func faultObservedFromError(err error) string {
	if err == nil {
		return "false"
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"timeout",
		"deadline exceeded",
		"connection refused",
		"connection reset",
		"no such host",
		"network",
		"eof",
		"i/o timeout",
		"context canceled",
		"provider:",     // wrapped provider errors from turn.go
		"subprocess",    // tool-shell-out failures
		"exit status",   // subprocess exit codes
	} {
		if strings.Contains(msg, marker) {
			return "true"
		}
	}
	return "false"
}

// turn is the legacy body signature (nil stats). Retained for
// call sites that don't need per-turn accumulation — internal
// callers should prefer turnWithStats.
func (a *Agent) turn(ctx context.Context, s *Session) (Message, error) {
	return a.turnWithStats(ctx, s, nil)
}

// turnWithStats is Turn's body, split out so Turn can bracket it
// with the progress turn_started / terminal pair on every exit
// path AND accumulate per-turn reliability counters (tokens,
// tool-call count) that Turn emits as Consistency resource_cv_*
// samples after the loop returns.
//
// stats may be nil — most callers pass nil; the outer Turn passes
// a fresh struct. The nil check is per-callsite for zero
// per-iteration overhead when accumulation is off.
func (a *Agent) turnWithStats(ctx context.Context, s *Session, stats *turnStats) (Message, error) {
	if len(s.Messages) == 0 {
		return Message{}, ErrEmptySession
	}

	if changed, err := a.opts.Compressor.Compress(ctx, s); err != nil {
		a.logger.Warn("agent.compress_failed", slog.String("err", err.Error()))
		observability.CompressorRewrites.WithLabelValues("error").Inc()
	} else if changed {
		a.logger.Info("agent.compressed", slog.Int("messages", len(s.Messages)))
		observability.CompressorRewrites.WithLabelValues("rewrote").Inc()
	} else {
		observability.CompressorRewrites.WithLabelValues("skipped").Inc()
	}

	toolDefs := a.registry.Definitions()

	for i := 0; i < a.opts.MaxIterations; i++ {
		// Mid-flight control: block while the user has the turn
		// paused, abort if they cancelled, and fold in anything they
		// steered into the turn since the last round-trip.
		if err := gate(ctx); err != nil {
			return Message{}, err
		}
		for _, m := range drainSteered(ctx) {
			s.Append(m)
		}

		req := Request{
			SessionID: s.ID,
			System:    a.systemPrompt(ctx, s),
			Messages:  s.Messages,
			Tools:     toolDefs,
		}

		a.emit(ctx, s, progress.Event{Kind: progress.KindThinking, Iteration: i + 1})
		start := time.Now()
		resp, err := a.provider.Complete(ctx, req)
		observability.ObserveProviderLatency(a.provider.Name(), "complete", start)
		if err != nil {
			observability.ProviderErrors.WithLabelValues(a.provider.Name(), "other").Inc()
			return Message{}, fmt.Errorf("provider: %w", err)
		}
		// Accumulate per-turn resource counters for Consistency
		// C_res: token totals and tool-call count. Silent no-op
		// when stats is nil so the legacy turn() call site pays
		// nothing extra.
		if stats != nil {
			stats.inputTokens += resp.Usage.InputTokens
			stats.outputTokens += resp.Usage.OutputTokens
			// A response with StopReason == StopToolUse triggered
			// at least one tool call — count it here so an
			// aborted-mid-tool turn still records the call
			// attempt.
			if resp.StopReason == StopToolUse {
				for _, c := range resp.Message.Content {
					if c.Kind == ContentToolUse {
						stats.toolCalls++
					}
				}
			}
		}

		// Record cost telemetry — best effort. Nil recorder disables.
		if a.opts.CostRecorder != nil {
			evt := CostEvent{
				SessionID: s.ID,
				Provider:  a.provider.Name(),
				Model:     resp.Model,
				Usage:     resp.Usage,
			}
			if rerr := a.opts.CostRecorder.Record(ctx, evt); rerr != nil {
				a.logger.Warn("agent.cost_record_failed",
					slog.String("session_id", s.ID),
					slog.String("err", rerr.Error()),
				)
			}
		}

		s.Append(resp.Message)

		if resp.StopReason == StopEndTurn {
			return resp.Message, nil
		}

		if resp.StopReason != StopToolUse {
			return resp.Message, nil
		}

		// Inject per-turn state that stateful tools (e.g. spawn_subagent)
		// may need. Stateless tools ignore these values.
		toolCtx := toolcontext.WithLogger(
			toolcontext.WithProvider(
				toolcontext.WithSession(ctx, s),
				a.provider),
			a.logger)

		results, err := a.runTools(toolCtx, resp.Message, s.ID)
		if err != nil {
			return Message{}, err
		}
		if len(results) > 0 {
			s.Append(Message{Role: RoleUser, Content: results})
		}
	}

	return Message{}, ErrMaxIterations
}

// systemPrompt composes the base system prompt with any appendix the
// configured SkillsProvider and RecallProvider choose to add. Called
// once per iteration so provider decisions react to the most recent
// user message.
//
// The context is intentionally the same one the Turn is running under;
// slow providers that block will delay the model round-trip and are
// caller-visible.
func (a *Agent) systemPrompt(ctx context.Context, s *Session) string {
	parts := make([]string, 0, 4)
	if a.opts.SystemPrompt != "" {
		parts = append(parts, a.opts.SystemPrompt)
	}
	if a.opts.SkillsProvider != nil {
		if x := a.opts.SkillsProvider.SystemAppendix(s); x != "" {
			parts = append(parts, x)
		}
	}
	if a.opts.RecallProvider != nil {
		if x := a.opts.RecallProvider.SystemAppendix(ctx, s); x != "" {
			parts = append(parts, x)
		}
	}
	if a.opts.EnableConfidenceElicitation {
		parts = append(parts, confidencePromptAddendum)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// confidencePromptAddendum is the one-shot instruction that turns
// on the Predictability "pair" signal. Deliberately terse — a few
// tokens per turn is worth the closed-loop calibration metric,
// but not a paragraph. Post-hoc self-assessment is the paper's
// (arXiv:2602.16666 §3.3) recommended elicitation shape:
// evaluate AFTER the task, not before.
const confidencePromptAddendum = `Close every reply with a single line containing your calibrated confidence that the response satisfies the user's request, in the exact form: <confidence>0.NN</confidence> where 0.00 = certain-wrong and 1.00 = certain-correct. The tag is machine-parsed for reliability metrics; do not add commentary before or after it. Do not mention the tag to the user.`

// confidenceRegex extracts the value inside <confidence>0.NN</confidence>.
// Tolerates spaces around the number, accepts 0.9 / 0.95 / 1 / 0 /
// .95, and permits a leading minus so a mis-calibrated model
// emitting "-0.3" still parses (the clamp in parseConfidence
// normalises to 0 — better than silently dropping the sample).
var confidenceRegex = regexp.MustCompile(`<confidence>\s*(-?[0-9]*\.?[0-9]+)\s*</confidence>`)

// parseConfidence returns the last <confidence>0.NN</confidence>
// value found in text, clamped to [0,1]. Returns (0, false) when
// no tag is present or the value is malformed. "Last" wins so a
// model that opens with a placeholder and closes with the real
// value still calibrates correctly.
func parseConfidence(text string) (float64, bool) {
	matches := confidenceRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	last := matches[len(matches)-1][1]
	v, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return 0, false
	}
	// Clamp to [0,1] — the aggregator's ECE/AUROC/Brier
	// implementations assume that domain.
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

func (a *Agent) runTools(ctx context.Context, m Message, sessionID string) ([]Content, error) {
	var results []Content
	for _, c := range m.Content {
		if c.Kind != ContentToolUse || c.ToolUse == nil {
			continue
		}
		// Between tool calls is the other safe point for pause/cancel.
		// Steered text is deliberately NOT drained here: the message
		// list must stay a well-formed tool_use/tool_result pair, so
		// injections wait for the iteration boundary.
		if err := gate(ctx); err != nil {
			return nil, err
		}
		use := c.ToolUse
		tool, ok := a.registry.Get(use.Name)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolNotFound, use.Name)
		}

		if decision, reason := a.opts.Approver.Approve(ctx, ApprovalRequest{
			ToolName:  use.Name,
			Input:     use.Input,
			SessionID: sessionID,
		}); decision == DecisionDeny {
			observability.ToolCalls.WithLabelValues(use.Name, "deny").Inc()
			if reason == "" {
				reason = "denied by policy"
			}
			a.logger.Warn("tool.denied", slog.String("name", use.Name), slog.String("reason", reason))
			a.emitEvent(ctx, progress.Event{Kind: progress.KindToolDenied, Tool: use.Name, Err: reason})
			a.emitAudit(ctx, "tool_call", "deny", use.Name, "denied", sessionID, map[string]any{
				"reason": reason,
			})
			results = append(results, Content{Kind: ContentToolResult, ToolResult: &ToolResult{
				ToolUseID: use.ID,
				Output:    "tool call blocked: " + reason,
				IsError:   true,
			}})
			continue
		}
		// PreToolUse hook — fires AFTER the Approver so operators can
		// layer policy-as-code on top of the pattern-based allow list.
		if a.opts.Hooks != nil {
			payload, mErr := hooks.MarshalPreToolUse(sessionID, use.Name, use.Input)
			if mErr != nil {
				// Should be impossible for well-formed input, but fail
				// open (log) rather than block the whole loop.
				a.logger.Warn("hook.marshal_failed", slog.String("event", string(hooks.EventPreToolUse)), slog.String("err", mErr.Error()))
			} else {
				verdict, hErr := a.opts.Hooks.Run(ctx, hooks.EventPreToolUse, payload)
				if hErr != nil {
					a.logger.Warn("hook.run_failed", slog.String("event", string(hooks.EventPreToolUse)), slog.String("err", hErr.Error()))
				}
				if verdict.Decision == hooks.DecisionDeny {
					observability.ToolCalls.WithLabelValues(use.Name, "hook_deny").Inc()
					reason := verdict.Reason
					if reason == "" {
						reason = "denied by hook"
					}
					a.logger.Warn("tool.hook_denied", slog.String("name", use.Name), slog.String("reason", reason))
					a.emitEvent(ctx, progress.Event{Kind: progress.KindToolDenied, Tool: use.Name, Err: reason})
					a.emitAudit(ctx, "tool_call", "deny", use.Name, "hook_denied", sessionID, map[string]any{
						"reason": reason,
					})
					results = append(results, Content{Kind: ContentToolResult, ToolResult: &ToolResult{
						ToolUseID: use.ID,
						Output:    "tool call blocked by hook: " + reason,
						IsError:   true,
					}})
					continue
				}
				if verdict.Decision == hooks.DecisionModify && len(verdict.Modified) > 0 {
					// A hook that wants to rewrite the input surfaces
					// the new input on `modified`; validate it parses
					// as JSON, otherwise leave the original untouched.
					var probe map[string]any
					if json.Unmarshal(verdict.Modified, &probe) == nil {
						use.Input = verdict.Modified
						a.logger.Info("tool.hook_modified", slog.String("name", use.Name))
					}
				}
			}
		}
		observability.ToolCalls.WithLabelValues(use.Name, "allow").Inc()

		a.logger.Info("tool.execute", slog.String("name", use.Name), slog.String("id", use.ID))
		detail := summarizeToolInput(use.Name, use.Input)
		a.emitEvent(ctx, progress.Event{Kind: progress.KindToolStarted, Tool: use.Name, Detail: detail})
		toolStart := time.Now()
		out, err := tool.Execute(ctx, use.Input)
		done := progress.Event{Kind: progress.KindToolFinished, Tool: use.Name, Detail: detail, Elapsed: time.Since(toolStart)}
		result := &ToolResult{ToolUseID: use.ID, Output: out}
		auditResult := "success"
		auditDetail := map[string]any{
			"elapsed_ms": time.Since(toolStart).Milliseconds(),
		}
		if err != nil {
			result.IsError = true
			result.Output = err.Error()
			done.Err = err.Error()
			a.logger.Warn("tool.error", slog.String("name", use.Name), slog.String("err", err.Error()))
			auditResult = "error"
			auditDetail["error"] = err.Error()
		}
		a.emitEvent(ctx, done)
		a.emitAudit(ctx, "tool_call", "run", use.Name, auditResult, sessionID, auditDetail)
		results = append(results, Content{Kind: ContentToolResult, ToolResult: result})
	}
	return results, nil
}

// emitAudit is a nil-safe helper that stamps a Record into the
// configured audit sink. Actor is drawn from
// [sso.IdentityFromContext] when available so downstream SIEMs
// can filter events by verified identity. Emit errors are
// deliberately swallowed — audit egress is best-effort
// observability; a downstream sink hiccup must not derail the
// agent loop. The sink's own dropped-record counter is the
// authoritative delivery signal.
func (a *Agent) emitAudit(ctx context.Context, category, verb, object, result, sessionID string, detail map[string]any) {
	if a.opts.AuditSink == nil {
		return
	}
	actor := "anonymous"
	if id, ok := sso.IdentityFromContext(ctx); ok && id.Subject != "" {
		actor = id.Subject
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["session_id"] = sessionID
	_ = a.opts.AuditSink.Emit(ctx, audit_egress.Record{ //nolint:errcheck // best-effort; sink internal counters authoritative
		Category: category,
		Actor:    actor,
		Verb:     verb,
		Object:   object,
		Result:   result,
		Detail:   detail,
	})
}
