// Package anthropic implements llm.Provider on top of the official
// anthropic-sdk-go client.
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// base64Encode wraps stdlib for readability at the call site.
func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Config configures the Anthropic Provider.
type Config struct {
	APIKey    string
	Model     string
	MaxTokens int64
}

// Provider is an llm.Provider backed by Anthropic's Claude API.
type Provider struct {
	client sdk.Client
	cfg    Config
}

// New constructs a Provider. APIKey is required; Model and MaxTokens
// fall back to sensible defaults.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: missing API key")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 4096
	}
	return &Provider{
		client: sdk.NewClient(option.WithAPIKey(cfg.APIKey)),
		cfg:    cfg,
	}, nil
}

// Name returns the provider's stable identifier.
func (p *Provider) Name() string { return "anthropic" }

// Complete runs a non-streaming completion.
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	msgs, err := toSDKMessages(req.Messages)
	if err != nil {
		return agent.Response{}, err
	}
	// Mark the requested leading window for the ephemeral prompt cache.
	// See docs/GAP_ANALYSIS_2026.md phase G. No-op when CacheableMessages
	// is 0 or exceeds len(msgs).
	applyCacheMarkers(msgs, req.CacheableMessages)

	params := sdk.MessageNewParams{
		Model:     p.cfg.Model,
		MaxTokens: p.cfg.MaxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		sys := sdk.TextBlockParam{Text: req.System}
		if req.CacheableMessages > 0 {
			// System prompt is stable across every turn of the daemon's
			// lifetime — 1-hour TTL amortises the cache-creation cost
			// over many completions.
			sys.CacheControl = cacheEphemeral1h
		}
		params.System = []sdk.TextBlockParam{sys}
	}
	if len(req.Tools) > 0 {
		params.Tools = toSDKTools(req.Tools, req.CacheableMessages > 0)
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return agent.Response{}, fmt.Errorf("anthropic: complete: %w", err)
	}

	assistant, err := fromSDKResponse(resp)
	if err != nil {
		return agent.Response{}, err
	}

	usage := agent.Usage{
		InputTokens:              int(resp.Usage.InputTokens),
		OutputTokens:             int(resp.Usage.OutputTokens),
		CacheCreationInputTokens: int(resp.Usage.CacheCreationInputTokens),
		CacheReadInputTokens:     int(resp.Usage.CacheReadInputTokens),
	}
	// Per-TTL cache-creation breakdown (only populated when the API
	// returned a CacheCreation subobject — old models / prompts w/o
	// caching leave it zero, so we conditionally read).
	usage.CacheCreationEphemeral1h = int(resp.Usage.CacheCreation.Ephemeral1hInputTokens)
	usage.CacheCreationEphemeral5m = int(resp.Usage.CacheCreation.Ephemeral5mInputTokens)

	// Emit prompt-cache telemetry so operators can chart hit ratio
	// per model. No-op when nothing was cached (all values zero).
	observability.ObservePromptCache(p.Name(), p.cfg.Model,
		usage.CacheReadInputTokens,
		usage.CacheCreationInputTokens,
		usage.CacheCreationEphemeral1h,
		usage.CacheCreationEphemeral5m,
	)

	return agent.Response{
		Message:    assistant,
		StopReason: mapStopReason(string(resp.StopReason)),
		Usage:      usage,
		Model:      p.cfg.Model,
	}, nil
}

func toSDKMessages(in []agent.Message) ([]sdk.MessageParam, error) {
	out := make([]sdk.MessageParam, 0, len(in))
	for _, m := range in {
		if m.Role == agent.RoleSystem {
			continue
		}
		blocks, err := toSDKContent(m.Content)
		if err != nil {
			return nil, err
		}
		switch m.Role {
		case agent.RoleUser:
			out = append(out, sdk.NewUserMessage(blocks...))
		case agent.RoleAssistant:
			out = append(out, sdk.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("anthropic: unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func toSDKContent(in []agent.Content) ([]sdk.ContentBlockParamUnion, error) {
	out := make([]sdk.ContentBlockParamUnion, 0, len(in))
	for _, c := range in {
		switch c.Kind {
		case agent.ContentText:
			out = append(out, sdk.NewTextBlock(c.Text))
		case agent.ContentImage:
			if c.Image == nil {
				return nil, errors.New("anthropic: image content missing payload")
			}
			out = append(out, sdk.NewImageBlockBase64(c.Image.MediaType, base64Encode(c.Image.Data)))
		case agent.ContentToolUse:
			if c.ToolUse == nil {
				return nil, errors.New("anthropic: tool_use content missing payload")
			}
			out = append(out, sdk.NewToolUseBlock(c.ToolUse.ID, c.ToolUse.Input, c.ToolUse.Name))
		case agent.ContentToolResult:
			if c.ToolResult == nil {
				return nil, errors.New("anthropic: tool_result content missing payload")
			}
			out = append(out, sdk.NewToolResultBlock(c.ToolResult.ToolUseID, c.ToolResult.Output, c.ToolResult.IsError))
		default:
			return nil, fmt.Errorf("anthropic: unsupported content kind %q", c.Kind)
		}
	}
	return out, nil
}

// toSDKTools converts the internal tool definitions to the SDK's
// wire shape. When cacheTools is true, the LAST tool in the list
// gets a 1-hour-TTL cache_control breakpoint — the API caches the
// prefix up to and including that block, which effectively means the
// whole tools array (and everything before it: the system prompt).
// Tools rarely change turn-to-turn so a long TTL amortises well.
func toSDKTools(in []tools.Definition, cacheTools bool) []sdk.ToolUnionParam {
	out := make([]sdk.ToolUnionParam, 0, len(in))
	for i, t := range in {
		toolParam := &sdk.ToolParam{
			Name:        t.Name,
			Description: sdk.String(t.Description),
			InputSchema: sdk.ToolInputSchemaParam{
				Properties: t.InputSchema["properties"],
			},
		}
		if cacheTools && i == len(in)-1 {
			toolParam.CacheControl = cacheEphemeral1h
		}
		out = append(out, sdk.ToolUnionParam{OfTool: toolParam})
	}
	return out
}

func fromSDKResponse(resp *sdk.Message) (agent.Message, error) {
	blocks := make([]agent.Content, 0, len(resp.Content))
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case sdk.TextBlock:
			blocks = append(blocks, agent.Content{Kind: agent.ContentText, Text: b.Text})
		case sdk.ToolUseBlock:
			raw, err := json.Marshal(b.Input)
			if err != nil {
				return agent.Message{}, fmt.Errorf("anthropic: marshal tool input: %w", err)
			}
			blocks = append(blocks, agent.Content{
				Kind:    agent.ContentToolUse,
				ToolUse: &agent.ToolUse{ID: b.ID, Name: b.Name, Input: raw},
			})
		default:
			return agent.Message{}, fmt.Errorf("anthropic: unsupported content block %T", b)
		}
	}
	return agent.Message{Role: agent.RoleAssistant, Content: blocks}, nil
}

func mapStopReason(s string) agent.StopReason {
	switch s {
	case "end_turn":
		return agent.StopEndTurn
	case "tool_use":
		return agent.StopToolUse
	case "max_tokens":
		return agent.StopMaxTokens
	default:
		return agent.StopOther
	}
}
