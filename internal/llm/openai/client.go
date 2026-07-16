// Package openai implements agent.Provider on top of the
// OpenAI-compatible Chat Completions API. BaseURL configuration means
// the same code serves OpenAI, OpenRouter, together.ai, deepinfra,
// ollama's OpenAI shim, and local LM Studio.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// base64Encode wraps stdlib for readability at the call site.
func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Config configures the provider.
type Config struct {
	// APIKey is the bearer token. Empty is rejected. For local ollama
	// with no auth, pass any non-empty placeholder.
	APIKey string
	// BaseURL overrides the endpoint. Empty uses OpenAI's default.
	// Common values:
	//   OpenRouter: https://openrouter.ai/api/v1
	//   ollama:     http://localhost:11434/v1
	//   LM Studio:  http://localhost:1234/v1
	//   together:   https://api.together.xyz/v1
	BaseURL string
	// Model is the model identifier passed to the API. Empty is
	// rejected — there is no universal default across providers.
	Model string
	// MaxTokens caps output tokens. Zero uses the SDK default.
	MaxTokens int64
	// Name is the provider name reported by Name(). Empty defaults to
	// "openai"; set it when you plan to distinguish OpenRouter /
	// ollama from OpenAI itself in logs and metrics.
	Name string
}

// Provider is an agent.Provider backed by the OpenAI Chat Completions
// API. Streaming lives in stream.go.
type Provider struct {
	client sdk.Client
	cfg    Config
}

// New constructs a Provider. APIKey and Model are required.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("openai: missing API key")
	}
	if cfg.Model == "" {
		return nil, errors.New("openai: missing model (there is no universal default)")
	}
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{client: sdk.NewClient(opts...), cfg: cfg}, nil
}

// Name returns the configured provider identifier.
func (p *Provider) Name() string { return p.cfg.Name }

// Complete runs a non-streaming completion via chat/completions.
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	msgs, err := toSDKMessages(req.System, req.Messages)
	if err != nil {
		return agent.Response{}, err
	}
	params := sdk.ChatCompletionNewParams{
		Model:    p.cfg.Model,
		Messages: msgs,
	}
	if p.cfg.MaxTokens > 0 {
		params.MaxTokens = sdk.Int(p.cfg.MaxTokens)
	}
	if len(req.Tools) > 0 {
		params.Tools = toSDKTools(req.Tools)
	}
	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return agent.Response{}, fmt.Errorf("openai: complete: %w", err)
	}
	return fromSDKResponse(resp)
}

// -- conversions -------------------------------------------------------

func toSDKMessages(system string, in []agent.Message) ([]sdk.ChatCompletionMessageParamUnion, error) {
	out := make([]sdk.ChatCompletionMessageParamUnion, 0, len(in)+1)
	if system != "" {
		out = append(out, sdk.SystemMessage(system))
	}
	for _, m := range in {
		converted, err := toSDKMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, converted...)
	}
	return out, nil
}

func toSDKMessage(m agent.Message) ([]sdk.ChatCompletionMessageParamUnion, error) {
	switch m.Role {
	case agent.RoleUser:
		return []sdk.ChatCompletionMessageParamUnion{userMessage(m.Content)}, nil
	case agent.RoleAssistant:
		text := collectText(m.Content)
		toolCalls := collectToolUses(m.Content)
		if len(toolCalls) == 0 {
			return []sdk.ChatCompletionMessageParamUnion{sdk.AssistantMessage(text)}, nil
		}
		msg := sdk.ChatCompletionAssistantMessageParam{
			ToolCalls: toolCalls,
		}
		if text != "" {
			msg.Content = sdk.ChatCompletionAssistantMessageParamContentUnion{
				OfString: sdk.String(text),
			}
		}
		return []sdk.ChatCompletionMessageParamUnion{
			{OfAssistant: &msg},
		}, nil
	case agent.RoleSystem:
		return []sdk.ChatCompletionMessageParamUnion{
			sdk.SystemMessage(collectText(m.Content)),
		}, nil
	}
	// Tool results — one message per tool_result block.
	var out []sdk.ChatCompletionMessageParamUnion
	for _, c := range m.Content {
		if c.Kind == agent.ContentToolResult && c.ToolResult != nil {
			out = append(out, sdk.ToolMessage(c.ToolResult.Output, c.ToolResult.ToolUseID))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openai: unsupported role %q", m.Role)
	}
	return out, nil
}

// userMessage constructs a user ChatCompletion message. When cs
// contains any image blocks the message uses the parts-array shape
// (text + image_url parts); otherwise a plain string message is
// returned for compatibility with older OpenAI-compatible endpoints
// that don't support the parts shape.
func userMessage(cs []agent.Content) sdk.ChatCompletionMessageParamUnion {
	// Fast path: no images → plain string content.
	hasImage := false
	for _, c := range cs {
		if c.Kind == agent.ContentImage {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return sdk.UserMessage(collectText(cs))
	}
	// Multipart content: preserve order across text and image blocks.
	parts := make([]sdk.ChatCompletionContentPartUnionParam, 0, len(cs))
	for _, c := range cs {
		switch c.Kind {
		case agent.ContentText:
			if c.Text != "" {
				parts = append(parts, sdk.TextContentPart(c.Text))
			}
		case agent.ContentImage:
			if c.Image == nil {
				continue
			}
			parts = append(parts, sdk.ImageContentPart(sdk.ChatCompletionContentPartImageImageURLParam{
				URL: fmt.Sprintf("data:%s;base64,%s", c.Image.MediaType,
					base64Encode(c.Image.Data)),
			}))
		}
	}
	return sdk.UserMessage(parts)
}

func collectText(cs []agent.Content) string {
	var s string
	for _, c := range cs {
		if c.Kind == agent.ContentText && c.Text != "" {
			if s != "" {
				s += "\n"
			}
			s += c.Text
		}
	}
	return s
}

func collectToolUses(cs []agent.Content) []sdk.ChatCompletionMessageToolCallParam {
	var out []sdk.ChatCompletionMessageToolCallParam
	for _, c := range cs {
		if c.Kind != agent.ContentToolUse || c.ToolUse == nil {
			continue
		}
		out = append(out, sdk.ChatCompletionMessageToolCallParam{
			ID: c.ToolUse.ID,
			Function: sdk.ChatCompletionMessageToolCallFunctionParam{
				Name:      c.ToolUse.Name,
				Arguments: string(c.ToolUse.Input),
			},
		})
	}
	return out
}

func toSDKTools(in []tools.Definition) []sdk.ChatCompletionToolParam {
	out := make([]sdk.ChatCompletionToolParam, 0, len(in))
	for _, t := range in {
		out = append(out, sdk.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: sdk.String(t.Description),
				Parameters:  shared.FunctionParameters(t.InputSchema),
			},
		})
	}
	return out
}

func fromSDKResponse(resp *sdk.ChatCompletion) (agent.Response, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return agent.Response{}, errors.New("openai: empty response")
	}
	choice := resp.Choices[0]
	msg := choice.Message

	blocks := make([]agent.Content, 0, 1+len(msg.ToolCalls))
	if msg.Content != "" {
		blocks = append(blocks, agent.Content{Kind: agent.ContentText, Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		fn := tc.Function
		blocks = append(blocks, agent.Content{
			Kind: agent.ContentToolUse,
			ToolUse: &agent.ToolUse{
				ID:    tc.ID,
				Name:  fn.Name,
				Input: json.RawMessage(fn.Arguments),
			},
		})
	}

	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: blocks,
		},
		StopReason: mapFinishReason(choice.FinishReason),
		Usage: agent.Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}, nil
}

func mapFinishReason(s string) agent.StopReason {
	switch s {
	case "stop":
		return agent.StopEndTurn
	case "tool_calls":
		return agent.StopToolUse
	case "length":
		return agent.StopMaxTokens
	default:
		return agent.StopOther
	}
}
