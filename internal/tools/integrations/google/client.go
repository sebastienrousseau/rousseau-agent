// Package google registers native Google Workspace tools (Gmail,
// Calendar, Drive) into the tool registry. Uses direct HTTP against
// the googleapis.com endpoints with a bearer token supplied via
// Config.AccessToken — production code populates this via the
// internal/auth/oauth broker after the operator's OAuth flow.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config carries the shared HTTP surface for every Google tool.
type Config struct {
	// AccessToken is a live bearer token. Callers should refresh via
	// the OAuth broker before every daemon-level batch of tool calls,
	// or supply a TokenFn.
	AccessToken string
	// TokenFn dynamically returns a fresh access token per request.
	// Preferred over AccessToken for long-running daemons because it
	// keeps the token off the Config struct's lifetime.
	TokenFn func(ctx context.Context) (string, error)
	// HTTPClient is injected in tests.
	HTTPClient *http.Client
	// GmailBaseURL overrides the Gmail API root. Set for tests.
	GmailBaseURL string
	// CalendarBaseURL overrides the Calendar API root. Set for tests.
	CalendarBaseURL string
	// DriveBaseURL overrides the Drive API root. Set for tests.
	DriveBaseURL string
}

// Defaults for the three API roots.
const (
	DefaultGmailBaseURL    = "https://gmail.googleapis.com/gmail/v1"
	DefaultCalendarBaseURL = "https://www.googleapis.com/calendar/v3"
	DefaultDriveBaseURL    = "https://www.googleapis.com/drive/v3"
)

// Client is a shared HTTP wrapper used by every Google tool.
type Client struct {
	tokenFn      func(ctx context.Context) (string, error)
	http         *http.Client
	gmailBase    string
	calendarBase string
	driveBase    string
}

// New constructs a Client. Either AccessToken or TokenFn must be set.
func New(cfg Config) (*Client, error) {
	c := &Client{
		http:         cfg.HTTPClient,
		gmailBase:    firstNonEmpty(cfg.GmailBaseURL, DefaultGmailBaseURL),
		calendarBase: firstNonEmpty(cfg.CalendarBaseURL, DefaultCalendarBaseURL),
		driveBase:    firstNonEmpty(cfg.DriveBaseURL, DefaultDriveBaseURL),
	}
	switch {
	case cfg.TokenFn != nil:
		c.tokenFn = cfg.TokenFn
	case cfg.AccessToken != "":
		c.tokenFn = func(context.Context) (string, error) { return cfg.AccessToken, nil }
	default:
		return nil, fmt.Errorf("google: AccessToken or TokenFn required")
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	return c, nil
}

// do issues a request with a bearer token and decodes the response.
func (c *Client) do(ctx context.Context, method, url string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("google: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("google: build request: %w", err)
	}
	tok, err := c.tokenFn(ctx)
	if err != nil {
		return fmt.Errorf("google: token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("google: %s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort read of error body
		return fmt.Errorf("google: %s %s: HTTP %d: %s", method, url, resp.StatusCode, string(snippet))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("google: decode response: %w", err)
		}
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}
