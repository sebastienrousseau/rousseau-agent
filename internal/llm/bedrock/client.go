// Package bedrock implements agent.Provider on top of AWS Bedrock's
// InvokeModel API. Bedrock hosts Anthropic's Claude models (and
// others) using SigV4 auth via the standard AWS SDK credential chain
// (env vars, ~/.aws/credentials, IMDS, IRSA on EKS, …).
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Config configures the provider.
type Config struct {
	// Region is the AWS region (e.g. "us-west-2"). Required.
	Region string
	// Model is the Bedrock model ID (e.g.
	// "anthropic.claude-sonnet-4-6-20260101-v1:0"). Required.
	Model string
	// Profile picks a credentials profile from ~/.aws/credentials.
	// Empty defers to the standard AWS credential chain.
	Profile string
	// MaxTokens caps output tokens. Zero uses the SDK default.
	MaxTokens int64
	// Runtime is optional — inject a fake for tests.
	Runtime InvokeAPI
}

// InvokeAPI is the narrow subset of *bedrockruntime.Client that the
// provider uses. Extracted so tests can inject a fake without
// standing up the SDK.
type InvokeAPI interface {
	InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, opts ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// Provider is an agent.Provider backed by AWS Bedrock.
type Provider struct {
	client InvokeAPI
	cfg    Config
}

// New constructs a Provider. Region and Model are required. When
// Config.Runtime is non-nil (test injection) it takes precedence over
// the SDK-constructed client.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Region == "" {
		return nil, errors.New("bedrock: Region is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("bedrock: Model is required")
	}
	client := cfg.Runtime
	if client == nil {
		opts := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion(cfg.Region),
		}
		if cfg.Profile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.Profile))
		}
		awscfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("bedrock: load AWS config: %w", err)
		}
		client = bedrockruntime.NewFromConfig(awscfg)
	}
	return &Provider{client: client, cfg: cfg}, nil
}

// Name returns the provider identifier.
func (*Provider) Name() string { return "bedrock" }

// Complete runs a non-streaming completion. The request body is the
// Anthropic-Bedrock JSON shape (`anthropic_version`, `messages`,
// `max_tokens`, …); other model families need a different converter.
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	body, err := buildBedrockBody(req, p.cfg.MaxTokens)
	if err != nil {
		return agent.Response{}, err
	}
	out, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     &p.cfg.Model,
		ContentType: awsString("application/json"),
		Accept:      awsString("application/json"),
		Body:        body,
	})
	if err != nil {
		return agent.Response{}, fmt.Errorf("bedrock: invoke: %w", err)
	}
	return parseBedrockResponse(out.Body)
}

// buildBedrockBody serialises the Anthropic-on-Bedrock JSON shape.
func buildBedrockBody(req agent.Request, maxTokens int64) ([]byte, error) {
	if maxTokens == 0 {
		maxTokens = 4096
	}
	messages := make([]bedrockMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == agent.RoleSystem {
			continue // system routed via top-level field
		}
		content, err := toBedrockContent(m.Content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, bedrockMessage{
			Role:    string(m.Role),
			Content: content,
		})
	}
	body := bedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		System:           req.System,
	}
	return json.Marshal(body)
}

func toBedrockContent(cs []agent.Content) ([]bedrockContent, error) {
	out := make([]bedrockContent, 0, len(cs))
	for _, c := range cs {
		switch c.Kind {
		case agent.ContentText:
			out = append(out, bedrockContent{Type: "text", Text: c.Text})
		case agent.ContentToolUse:
			if c.ToolUse == nil {
				return nil, errors.New("bedrock: tool_use content missing payload")
			}
			var input any
			if len(c.ToolUse.Input) > 0 {
				if err := json.Unmarshal(c.ToolUse.Input, &input); err != nil {
					return nil, fmt.Errorf("bedrock: tool_use input: %w", err)
				}
			}
			out = append(out, bedrockContent{
				Type: "tool_use",
				ID:   c.ToolUse.ID, Name: c.ToolUse.Name, Input: input,
			})
		case agent.ContentToolResult:
			if c.ToolResult == nil {
				return nil, errors.New("bedrock: tool_result content missing payload")
			}
			out = append(out, bedrockContent{
				Type: "tool_result", ToolUseID: c.ToolResult.ToolUseID,
				Content: c.ToolResult.Output, IsError: c.ToolResult.IsError,
			})
		default:
			return nil, fmt.Errorf("bedrock: unsupported content kind %q", c.Kind)
		}
	}
	return out, nil
}

// parseBedrockResponse converts the Anthropic-on-Bedrock response
// JSON into an agent.Response.
func parseBedrockResponse(body []byte) (agent.Response, error) {
	var raw bedrockResponse
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return agent.Response{}, fmt.Errorf("bedrock: parse response: %w", err)
	}
	blocks := make([]agent.Content, 0, len(raw.Content))
	for _, c := range raw.Content {
		switch c.Type {
		case "text":
			if c.Text == "" {
				continue
			}
			blocks = append(blocks, agent.Content{Kind: agent.ContentText, Text: c.Text})
		case "tool_use":
			input, err := json.Marshal(c.Input)
			if err != nil {
				return agent.Response{}, fmt.Errorf("bedrock: tool_use input: %w", err)
			}
			blocks = append(blocks, agent.Content{
				Kind: agent.ContentToolUse,
				ToolUse: &agent.ToolUse{
					ID: c.ID, Name: c.Name, Input: input,
				},
			})
		}
	}
	return agent.Response{
		Message:    agent.Message{Role: agent.RoleAssistant, Content: blocks},
		StopReason: mapStop(raw.StopReason),
		Usage: agent.Usage{
			InputTokens:  raw.Usage.InputTokens,
			OutputTokens: raw.Usage.OutputTokens,
		},
	}, nil
}

func mapStop(s string) agent.StopReason {
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

func awsString(s string) *string { return &s }

// -- wire types --------------------------------------------------------

type bedrockRequest struct {
	AnthropicVersion string           `json:"anthropic_version"`
	MaxTokens        int64            `json:"max_tokens"`
	System           string           `json:"system,omitempty"`
	Messages         []bedrockMessage `json:"messages"`
}

type bedrockMessage struct {
	Role    string           `json:"role"`
	Content []bedrockContent `json:"content"`
}

type bedrockContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type bedrockResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	StopReason string           `json:"stop_reason"`
	Content    []bedrockContent `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
