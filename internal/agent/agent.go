package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Options tunes the Agent loop.
type Options struct {
	// MaxIterations caps how many model round-trips a single Turn may
	// perform. Zero uses the default (32).
	MaxIterations int
	// SystemPrompt is prepended to every request.
	SystemPrompt string
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
func (a *Agent) Turn(ctx context.Context, s *Session) (Message, error) {
	if len(s.Messages) == 0 {
		return Message{}, ErrEmptySession
	}

	toolDefs := a.registry.Definitions()

	for i := 0; i < a.opts.MaxIterations; i++ {
		req := Request{
			SessionID: s.ID,
			System:    a.opts.SystemPrompt,
			Messages:  s.Messages,
			Tools:     toolDefs,
		}

		resp, err := a.provider.Complete(ctx, req)
		if err != nil {
			return Message{}, fmt.Errorf("provider: %w", err)
		}

		s.Append(resp.Message)

		if resp.StopReason == StopEndTurn {
			return resp.Message, nil
		}

		if resp.StopReason != StopToolUse {
			return resp.Message, nil
		}

		results, err := a.runTools(ctx, resp.Message)
		if err != nil {
			return Message{}, err
		}
		if len(results) > 0 {
			s.Append(Message{Role: RoleUser, Content: results})
		}
	}

	return Message{}, ErrMaxIterations
}

func (a *Agent) runTools(ctx context.Context, m Message) ([]Content, error) {
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
