// Package composio adapts Composio's tool broker as an opt-in tool
// provider. When enabled, every tool the operator has authorised in
// Composio's console (Gmail, GitHub, Slack, Notion, Linear, Calendar,
// Drive, Stripe, HubSpot, Salesforce, ClickUp, Airtable, and hundreds
// more — the "1000+ integrations" surface named in
// docs/COMPETITORS_2026_07_12.md) becomes a rousseau tool the model
// can invoke.
//
// This closes the Row 5 (Feature breadth) gap flagged in the 5-week
// campaign release notes without moving the daemon onto Vercel /
// Neon / Upstash — rousseau keeps its single-container deployment
// story and Composio becomes a documented, opt-in adapter.
package composio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DefaultBaseURL is Composio's REST endpoint. Override for tests.
const DefaultBaseURL = "https://backend.composio.dev/api/v3"

// EnvAPIKey is the environment variable read as a fallback for
// Config.APIKey.
const EnvAPIKey = "ROUSSEAU_COMPOSIO_API_KEY"

// EnvUserID is the environment variable read as a fallback for
// Config.UserID. Composio's multi-user model requires an id so a
// tool call targets the right OAuth account.
const EnvUserID = "ROUSSEAU_COMPOSIO_USER_ID"

// Config configures the Composio client.
type Config struct {
	// APIKey — a Composio API key. Empty falls back to
	// $ROUSSEAU_COMPOSIO_API_KEY.
	APIKey string
	// UserID — the Composio user id whose OAuth tokens are used for
	// every executed action. Empty falls back to
	// $ROUSSEAU_COMPOSIO_USER_ID. Required — Composio's execution
	// endpoint refuses anonymous calls.
	UserID string
	// AppFilter, when non-empty, restricts the auto-registered tool
	// set to the named Composio apps (e.g. []string{"gmail", "slack",
	// "github"}). Empty registers every app the API returns —
	// convenient for exploration, dangerous for auditing.
	AppFilter []string
	// BaseURL overrides DefaultBaseURL — set for tests.
	BaseURL string
	// HTTPClient is injected in tests.
	HTTPClient *http.Client
}

// Client is a thin Composio REST client scoped to the four
// operations the tool adapter needs: list-actions, describe-action,
// execute-action, and health-check.
type Client struct {
	apiKey  string
	userID  string
	baseURL string
	http    *http.Client
}

// New constructs a Client. Both APIKey and UserID (or their env
// fallbacks) are required.
func New(cfg Config) (*Client, error) {
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv(EnvAPIKey)
	}
	if key == "" {
		return nil, fmt.Errorf("composio: APIKey required (set $%s or Config.APIKey)", EnvAPIKey)
	}
	uid := cfg.UserID
	if uid == "" {
		uid = os.Getenv(EnvUserID)
	}
	if uid == "" {
		return nil, fmt.Errorf("composio: UserID required (set $%s or Config.UserID)", EnvUserID)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second} // Composio proxies to third-party APIs; 60s is safer than 30s.
	}
	return &Client{apiKey: key, userID: uid, baseURL: base, http: h}, nil
}

// Action is Composio's shape for one discoverable tool. Kept
// minimal — the model consumes Name / Description / Parameters.
type Action struct {
	Name        string          `json:"name"`
	AppKey      string          `json:"appKey"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// List returns every action the authenticated user can invoke.
// Composio's endpoint supports pagination via a cursor; the current
// implementation stops after the first page (default 100 actions),
// which is enough for most operators. Callers that need the full
// long tail should iterate ListPage themselves.
func (c *Client) List(ctx context.Context) ([]Action, error) {
	return c.ListPage(ctx, "")
}

// ListPage returns one page starting at cursor. Empty cursor asks
// for the first page. The returned nextCursor is empty on the last
// page.
func (c *Client) ListPage(ctx context.Context, cursor string) ([]Action, error) {
	path := "/actions"
	if cursor != "" {
		path += "?cursor=" + cursor
	}
	var out struct {
		Items []Action `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Execute invokes an action by name with the supplied parameters and
// returns the raw JSON response body. The adapter passes this
// through to the model as the tool result; Composio-side errors
// surface as non-2xx responses that this method wraps.
func (c *Client) Execute(ctx context.Context, actionName string, params json.RawMessage) (json.RawMessage, error) {
	body := map[string]any{
		"userId":     c.userID,
		"actionName": actionName,
	}
	if len(params) > 0 {
		body["input"] = params
	}
	var out json.RawMessage
	if err := c.do(ctx, http.MethodPost, "/actions/execute", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// do is the generic request path. Bearer-token auth, 8-KB error
// snippet on non-2xx.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("composio: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("composio: build request: %w", err)
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("composio: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort read of error body
		return fmt.Errorf("composio: %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(snippet))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("composio: decode response: %w", err)
		}
	}
	return nil
}
