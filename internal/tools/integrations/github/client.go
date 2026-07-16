// Package github registers native GitHub tools into the tool
// registry. Uses direct HTTP against api.github.com to keep binary
// size small — the operator authenticates with a Personal Access
// Token (classic or fine-grained) supplied via config or env var.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is the api.github.com host. Override for GitHub
// Enterprise Server.
const DefaultBaseURL = "https://api.github.com"

// EnvToken is the environment variable read as a fallback for
// Config.Token.
const EnvToken = "ROUSSEAU_GITHUB_TOKEN"

// Config configures a GitHub API client.
type Config struct {
	// Token is a Personal Access Token. Fine-grained or classic both
	// work. Empty falls back to $ROUSSEAU_GITHUB_TOKEN.
	Token string
	// BaseURL overrides DefaultBaseURL — set for GHES installs.
	BaseURL string
	// HTTPClient is injected in tests. nil uses a default 30s-timeout
	// client.
	HTTPClient *http.Client
	// UserAgent is sent on every request. Empty uses
	// "rousseau-agent-tools/1".
	UserAgent string
}

// Client is a thin GitHub API client scoped to the operations the
// registered tools need. Safe for concurrent use.
type Client struct {
	token     string
	baseURL   string
	http      *http.Client
	userAgent string
}

// New constructs a Client. A missing token in cfg is filled from
// $ROUSSEAU_GITHUB_TOKEN; an entirely-empty token returns an error
// because every GitHub tool needs authentication.
func New(cfg Config) (*Client, error) {
	tok := cfg.Token
	if tok == "" {
		tok = envToken()
	}
	if tok == "" {
		return nil, fmt.Errorf("github: token required (set $%s or Config.Token)", EnvToken)
	}
	c := &Client{
		token:     tok,
		baseURL:   firstNonEmpty(cfg.BaseURL, DefaultBaseURL),
		http:      cfg.HTTPClient,
		userAgent: firstNonEmpty(cfg.UserAgent, "rousseau-agent-tools/1"),
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	return c, nil
}

// do issues a request and JSON-decodes the response body into out.
// A nil out skips decoding. Non-2xx responses return an error
// carrying the response body verbatim (bounded to 8 KB).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort read of error body
		return fmt.Errorf("github: %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(snippet))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("github: decode response: %w", err)
		}
	}
	return nil
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
