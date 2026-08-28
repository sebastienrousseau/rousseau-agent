// Package sms implements a send-only transport for SMS delivery via
// Twilio or Vonage.
//
// Inbound SMS is not supported: it requires a public HTTP surface for
// the carrier's webhook, which conflicts with rousseau's "no HTTP
// surface by default" posture. The cron scheduler and any code that
// wants to text a phone number goes through Deliver.
//
// The transport.Transport interface's Start method is implemented as
// a no-op that blocks on ctx.Done() — this keeps sms compatible with
// the daemonWiring shape while making its send-only nature explicit.
package sms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Provider selects which carrier to use.
type Provider string

const (
	// ProviderTwilio uses Twilio's REST API.
	ProviderTwilio Provider = "twilio"
	// ProviderVonage uses Vonage's (formerly Nexmo) REST API.
	ProviderVonage Provider = "vonage"
)

// Config configures the SMS transport.
type Config struct {
	// Provider selects the carrier. Required.
	Provider Provider
	// From is the sending phone number (E.164, e.g. "+15551234567")
	// or Twilio Messaging Service SID.
	From string
	// AccountSID is Twilio's account SID (starts with "AC…"). Only
	// used when Provider=twilio.
	AccountSID string
	// AuthToken is the Twilio auth token or the Vonage API secret.
	AuthToken string
	// APIKey is the Vonage API key. Only used when Provider=vonage.
	APIKey string
	// BaseURL overrides the REST endpoint. Empty defaults to the
	// carrier's public API.
	BaseURL string
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string
	// HTTPClient overrides the transport. Zero uses 30s timeout.
	HTTPClient *http.Client
}

// Client is a transport.Transport backed by an SMS carrier.
type Client struct {
	cfg    Config
	logger *slog.Logger
	http   *http.Client
}

// New constructs a Client.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.Provider == "" {
		return nil, errors.New("sms: Provider is required")
	}
	if cfg.From == "" {
		return nil, errors.New("sms: From is required")
	}
	switch cfg.Provider {
	case ProviderTwilio:
		if cfg.AccountSID == "" || cfg.AuthToken == "" {
			return nil, errors.New("sms: twilio requires AccountSID + AuthToken")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.twilio.com/2010-04-01"
		}
	case ProviderVonage:
		if cfg.APIKey == "" || cfg.AuthToken == "" {
			return nil, errors.New("sms: vonage requires APIKey + AuthToken (API secret)")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://rest.nexmo.com"
		}
	default:
		return nil, fmt.Errorf("sms: unknown provider %q", cfg.Provider)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient}, nil
}

// Name returns the transport identifier.
func (c *Client) Name() string { return "sms-" + string(c.cfg.Provider) }

// Start blocks until ctx is cancelled. SMS is send-only; inbound
// requires public HTTP infrastructure the daemon does not expose.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("sms: handler is required")
	}
	c.logger.Info("sms.started",
		slog.String("provider", string(c.cfg.Provider)),
		slog.String("mode", "send-only"))
	<-ctx.Done()
	return ctx.Err()
}

// Stop is a no-op — there is no persistent connection.
func (*Client) Stop() error { return nil }

// Deliver sends a text message to the given phone number.
func (c *Client) Deliver(ctx context.Context, to, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	switch c.cfg.Provider {
	case ProviderTwilio:
		return c.sendTwilio(ctx, to, body)
	case ProviderVonage:
		return c.sendVonage(ctx, to, body)
	}
	return fmt.Errorf("sms: unknown provider %q", c.cfg.Provider)
}

func (c *Client) sendTwilio(ctx context.Context, to, body string) error {
	form := url.Values{}
	form.Set("To", to)
	form.Set("From", c.cfg.From)
	form.Set("Body", body)
	endpoint := fmt.Sprintf("%s/Accounts/%s/Messages.json", c.cfg.BaseURL, c.cfg.AccountSID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("sms: twilio: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.cfg.AccountSID, c.cfg.AuthToken)
	return c.send(req, "twilio")
}

func (c *Client) sendVonage(ctx context.Context, to, body string) error {
	form := url.Values{}
	form.Set("to", strings.TrimPrefix(to, "+"))
	form.Set("from", strings.TrimPrefix(c.cfg.From, "+"))
	form.Set("text", body)
	form.Set("api_key", c.cfg.APIKey)
	form.Set("api_secret", c.cfg.AuthToken)
	endpoint := c.cfg.BaseURL + "/sms/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("sms: vonage: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.send(req, "vonage")
}

// send is the shared HTTP execution + error-surfacing path.
func (c *Client) send(req *http.Request, provider string) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sms: %s: %w", provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("sms: %s: read: %w", provider, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sms: %s: HTTP %d: %s", provider, resp.StatusCode, truncate(string(rb), 400))
	}
	// Vonage returns 200 with an error struct inside the body — parse
	// and surface if present.
	if provider == "vonage" {
		var v struct {
			Messages []struct {
				Status    string `json:"status"`
				ErrorText string `json:"error-text"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(rb, &v); err == nil {
			for _, m := range v.Messages {
				if m.Status != "0" {
					return fmt.Errorf("sms: vonage: status=%s: %s", m.Status, m.ErrorText)
				}
			}
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
