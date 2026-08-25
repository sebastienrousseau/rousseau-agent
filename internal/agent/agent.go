package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
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
	// Hooks is the lifecycle-hook runner. When non-nil the agent
	// loop consults it at pre_tool_use before each tool call.
	// A Deny verdict blocks the tool and surfaces the reason to the
	// model as a synthetic tool-result error. Nil disables hooks
	// entirely.
	Hooks hooks.Runner
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
		req := Request{
			SessionID: s.ID,
			System:    a.systemPrompt(ctx, s),
			Messages:  s.Messages,
			Tools:     toolDefs,
		}

		start := time.Now()
		resp, err := a.provider.Complete(ctx, req)
		observability.ObserveProviderLatency(a.provider.Name(), "complete", start)
		if err != nil {
			observability.ProviderErrors.WithLabelValues(a.provider.Name(), "other").Inc()
			return Message{}, fmt.Errorf("provider: %w", err)
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
	parts := make([]string, 0, 3)
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
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

func (a *Agent) runTools(ctx context.Context, m Message, sessionID string) ([]Content, error) {
	var results []Content
	for _, c := range m.Content {
		if c.Kind != ContentToolUse || c.ToolUse == nil {
			continue
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
		out, err := tool.Execute(ctx, use.Input)
		result := &ToolResult{ToolUseID: use.ID, Output: out}
		if err != nil {
			result.IsError = true
			result.Output = err.Error()
			a.logger.Warn("tool.error", slog.String("name", use.Name), slog.String("err", err.Error()))
		}
		results = append(results, Content{Kind: ContentToolResult, ToolResult: result})
	}
	return results, nil
}
