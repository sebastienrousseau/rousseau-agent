// Package slack registers native Slack tools (post_message,
// get_thread, add_reaction, list_channels) into the tool registry.
// Uses direct HTTP against slack.com/api with a bot token.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Slack Web API base. Override for testing.
const DefaultBaseURL = "https://slack.com/api"

// EnvToken is the environment variable read as a fallback for
// Config.BotToken.
const EnvToken = "ROUSSEAU_SLACK_TOOLS_TOKEN"

// Config configures the Slack tool client.
type Config struct {
	// BotToken is an xoxb- token. Empty falls back to
	// $ROUSSEAU_SLACK_TOOLS_TOKEN. Distinct from the Socket Mode
	// tokens the slack transport uses so a read/write bot can live
	// alongside a Socket Mode connection.
	BotToken string
	// BaseURL overrides DefaultBaseURL — set for tests.
	BaseURL string
	// HTTPClient is injected in tests.
	HTTPClient *http.Client
}

// Client is a thin Slack Web API client.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// New constructs a Client. Empty BotToken falls back to the env var.
func New(cfg Config) (*Client, error) {
	tok := cfg.BotToken
	if tok == "" {
		tok = envToken()
	}
	if tok == "" {
		return nil, fmt.Errorf("slack: bot token required (set $%s or Config.BotToken)", EnvToken)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{token: tok, baseURL: base, http: h}, nil
}

// slackEnvelope is the top-level Slack Web API response shape.
type slackEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// postForm issues a POST with application/x-www-form-urlencoded body
// (Slack's classic API shape) and decodes the response into out.
func (c *Client) postForm(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

// postJSON issues a POST with a JSON body (Slack's newer methods).
func (c *Client) postJSON(ctx context.Context, method string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("slack: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return c.do(req, out)
}

// get issues a GET (used by list_channels, get_thread).
func (c *Client) get(ctx context.Context, method string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/"+method+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.do(req, out)
}

// do runs req, checks slackEnvelope.OK, and unmarshals into out.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slack: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack: %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, string(body))
	}
	// Every Slack Web API method returns {"ok": bool, "error": string,
	// ...}. Peek at the envelope even when out is nil.
	var env slackEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("slack: decode envelope: %w", err)
	}
	if !env.OK {
		return fmt.Errorf("slack: %s: %s", req.URL.Path, env.Error)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("slack: decode payload: %w", err)
		}
	}
	return nil
}
