// Package linear registers native Linear tools (list_issues,
// get_issue, create_issue, update_issue) into the tool registry.
// Uses Linear's single-endpoint GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is the Linear GraphQL endpoint.
const DefaultBaseURL = "https://api.linear.app/graphql"

// EnvToken is the environment variable read as a fallback for
// Config.APIKey.
const EnvToken = "ROUSSEAU_LINEAR_API_KEY"

// Config configures the Linear client.
type Config struct {
	// APIKey is a Linear personal API key (starts "lin_api_"). Empty
	// falls back to $ROUSSEAU_LINEAR_API_KEY.
	APIKey string
	// BaseURL overrides DefaultBaseURL — set for tests.
	BaseURL string
	// HTTPClient is injected in tests.
	HTTPClient *http.Client
}

// Client wraps Linear's GraphQL endpoint with typed helpers.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New constructs a Client. Empty APIKey falls back to the env var.
func New(cfg Config) (*Client, error) {
	key := cfg.APIKey
	if key == "" {
		key = envKey()
	}
	if key == "" {
		return nil, fmt.Errorf("linear: API key required (set $%s or Config.APIKey)", EnvToken)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{apiKey: key, baseURL: base, http: h}, nil
}

// gqlEnvelope is Linear's standard GraphQL response shape.
type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// query runs a GraphQL query with the supplied variables and decodes
// the `data` field into out.
func (c *Client) query(ctx context.Context, query string, variables map[string]any, out any) error {
	body := map[string]any{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("linear: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("linear: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("linear: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var env gqlEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("linear: decode envelope: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("linear: %s", env.Errors[0].Message)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("linear: decode data: %w", err)
		}
	}
	return nil
}
