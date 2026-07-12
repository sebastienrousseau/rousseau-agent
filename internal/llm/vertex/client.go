// Package vertex implements agent.Provider on top of Google Vertex
// AI's Anthropic-on-Vertex REST endpoint.
//
// Wire format is the standard Anthropic messages JSON (same as
// Bedrock); auth is Google Application Default Credentials via
// golang.org/x/oauth2/google. Endpoint layout:
//
//     https://<region>-aiplatform.googleapis.com/v1/
//         projects/<project>/locations/<region>/publishers/anthropic/
//         models/<model>:rawPredict
package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2/google"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Config configures the Vertex provider.
type Config struct {
	// Project is the Google Cloud project id. Required.
	Project string
	// Region is the Vertex region (e.g. "us-central1"). Required.
	Region string
	// Model is the Anthropic model id as Vertex publishes it (e.g.
	// "claude-sonnet-4-6@20260101"). Required.
	Model string
	// CredentialsFile optionally points at a service-account JSON key.
	// Empty defers to Application Default Credentials.
	CredentialsFile string
	// MaxTokens caps output tokens. Zero uses 4096.
	MaxTokens int64

	// HTTPClient overrides the transport used to reach Vertex. Zero
	// resolves credentials via ADC and installs an oauth2 transport.
	// Tests inject a client backed by httptest.Server.
	HTTPClient *http.Client
}

// Provider is an agent.Provider backed by Vertex AI.
type Provider struct {
	http *http.Client
	cfg  Config
	url  string
}

// New constructs a Provider. Project, Region, and Model are required.
// When HTTPClient is nil, credentials come from ADC (env
// GOOGLE_APPLICATION_CREDENTIALS, gcloud user credentials, or the
// GCE/GKE metadata server).
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Project == "" {
		return nil, errors.New("vertex: Project is required")
	}
	if cfg.Region == "" {
		return nil, errors.New("vertex: Region is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("vertex: Model is required")
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 4096
	}

	client := cfg.HTTPClient
	if client == nil {
		scopes := []string{"https://www.googleapis.com/auth/cloud-platform"}
		var (
			creds *google.Credentials
			err   error
		)
		if cfg.CredentialsFile != "" {
			data, rerr := readAllFile(cfg.CredentialsFile)
			if rerr != nil {
				return nil, fmt.Errorf("vertex: read credentials: %w", rerr)
			}
			creds, err = google.CredentialsFromJSON(ctx, data, scopes...)
		} else {
			creds, err = google.FindDefaultCredentials(ctx, scopes...)
		}
		if err != nil {
			return nil, fmt.Errorf("vertex: credentials: %w", err)
		}
		client = oauth2Client(ctx, creds)
	}

	url := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		cfg.Region, cfg.Project, cfg.Region, cfg.Model,
	)
	return &Provider{http: client, cfg: cfg, url: url}, nil
}

// Name returns the provider identifier.
func (*Provider) Name() string { return "vertex" }

// Complete runs a non-streaming completion.
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	body, err := buildVertexBody(req, p.cfg.MaxTokens)
	if err != nil {
		return agent.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return agent.Response{}, fmt.Errorf("vertex: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return agent.Response{}, fmt.Errorf("vertex: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Response{}, fmt.Errorf("vertex: read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return agent.Response{}, fmt.Errorf("vertex: HTTP %d: %s", resp.StatusCode, truncate(string(rb), 400))
	}
	return parseVertexResponse(rb)
}

// -- wire (mirrors Bedrock's Anthropic shape, minus the
// anthropic_version field which Vertex encodes differently) ------------

func buildVertexBody(req agent.Request, maxTokens int64) ([]byte, error) {
	msgs := make([]vertexMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == agent.RoleSystem {
			continue
		}
		content, err := toVertexContent(m.Content)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, vertexMessage{Role: string(m.Role), Content: content})
	}
	return json.Marshal(vertexRequest{
		AnthropicVersion: "vertex-2023-10-16",
		MaxTokens:        maxTokens,
		System:           req.System,
		Messages:         msgs,
	})
}

func toVertexContent(cs []agent.Content) ([]vertexContent, error) {
	out := make([]vertexContent, 0, len(cs))
	for _, c := range cs {
		switch c.Kind {
		case agent.ContentText:
			out = append(out, vertexContent{Type: "text", Text: c.Text})
		case agent.ContentToolUse:
			if c.ToolUse == nil {
				return nil, errors.New("vertex: tool_use content missing payload")
			}
			var input any
			if len(c.ToolUse.Input) > 0 {
				if err := json.Unmarshal(c.ToolUse.Input, &input); err != nil {
					return nil, fmt.Errorf("vertex: tool_use input: %w", err)
				}
			}
			out = append(out, vertexContent{
				Type: "tool_use", ID: c.ToolUse.ID, Name: c.ToolUse.Name, Input: input,
			})
		case agent.ContentToolResult:
			if c.ToolResult == nil {
				return nil, errors.New("vertex: tool_result content missing payload")
			}
			out = append(out, vertexContent{
				Type: "tool_result", ToolUseID: c.ToolResult.ToolUseID,
				Content: c.ToolResult.Output, IsError: c.ToolResult.IsError,
			})
		default:
			return nil, fmt.Errorf("vertex: unsupported content kind %q", c.Kind)
		}
	}
	return out, nil
}

func parseVertexResponse(body []byte) (agent.Response, error) {
	var raw vertexResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return agent.Response{}, fmt.Errorf("vertex: parse: %w", err)
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
				return agent.Response{}, fmt.Errorf("vertex: tool_use input: %w", err)
			}
			blocks = append(blocks, agent.Content{
				Kind:    agent.ContentToolUse,
				ToolUse: &agent.ToolUse{ID: c.ID, Name: c.Name, Input: input},
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// -- wire types --------------------------------------------------------

type vertexRequest struct {
	AnthropicVersion string          `json:"anthropic_version"`
	MaxTokens        int64           `json:"max_tokens"`
	System           string          `json:"system,omitempty"`
	Messages         []vertexMessage `json:"messages"`
}

type vertexMessage struct {
	Role    string          `json:"role"`
	Content []vertexContent `json:"content"`
}

type vertexContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type vertexResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	StopReason string          `json:"stop_reason"`
	Content    []vertexContent `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
