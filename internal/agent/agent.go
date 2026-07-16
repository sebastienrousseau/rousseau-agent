package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
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

		s.Append(resp.Message)

		if resp.StopReason == StopEndTurn {
			return resp.Message, nil
		}

		if resp.StopReason != StopToolUse {
			return resp.Message, nil
		}

		results, err := a.runTools(ctx, resp.Message, s.ID)
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
