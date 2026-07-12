// Package slack implements transport.Transport against Slack's Socket
// Mode + Web API. Socket Mode gives us bi-directional traffic with
// zero public HTTP surface (no webhook receiver needed); the Web API
// covers outbound chat.postMessage.
//
// Prerequisites the operator arranges out-of-band:
//   - A Slack app with Socket Mode enabled
//   - App-level token (xapp-*) with `connections:write`
//   - Bot token (xoxb-*) with `chat:write` + event subscriptions for
//     `message.channels`, `message.im`, or whichever channel scopes the
//     bot should hear
//   - Install the app to a workspace
//
// Wire format is JSON-RPC-ish envelopes over WebSocket for inbound,
// standard HTTP POST for outbound.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the Slack transport.
type Config struct {
	// AppToken is the xapp-* app-level token with connections:write.
	AppToken string
	// BotToken is the xoxb-* bot token with chat:write.
	BotToken string
	// BotUserID is the bot user's own user ID ("U01…"). Optional, used
	// to skip own-message echo. If unset, Slack's own `bot_id` field
	// is checked instead.
	BotUserID string
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string
	// BaseURL overrides the Web API endpoint. Empty defaults to
	// https://slack.com/api. Tests inject an httptest URL.
	BaseURL string
	// HTTPClient overrides the transport for Web API + Socket Mode
	// connection open. Zero uses a 30s-timeout client.
	HTTPClient *http.Client
	// DialWebSocket overrides the WebSocket dialer. Zero uses
	// github.com/coder/websocket. Tests inject a stub that returns a
	// scripted event stream.
	DialWebSocket func(ctx context.Context, url string) (WSConn, error)
}

// WSConn is the narrow subset of *websocket.Conn the transport uses.
// Extracted so tests can drive the Socket Mode side without a real
// server.
type WSConn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close(code websocket.StatusCode, reason string) error
}

// Client is a transport.Transport backed by Slack Socket Mode.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	http    *http.Client
	stopped atomic.Bool

	mu   sync.Mutex
	conn WSConn
}

// New constructs a Client. AppToken and BotToken are required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.AppToken == "" {
		return nil, errors.New("slack: AppToken (xapp-*) is required")
	}
	if cfg.BotToken == "" {
		return nil, errors.New("slack: BotToken (xoxb-*) is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://slack.com/api"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.DialWebSocket == nil {
		cfg.DialWebSocket = defaultDial
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "slack" }

// Start opens Socket Mode and routes events to the handler until ctx
// is cancelled.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("slack: handler is required")
	}
	c.logger.Info("slack.started")
	for {
		if c.stopped.Load() || ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.runOnce(ctx, handler); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logger.Warn("slack.session_failed", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
	}
}

// Stop halts the sesion loop and closes the current connection.
func (c *Client) Stop() error {
	c.stopped.Store(true)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close(websocket.StatusNormalClosure, "shutdown")
		c.conn = nil
	}
	return nil
}

// runOnce opens Socket Mode once and pumps events until an error or
// context cancellation.
func (c *Client) runOnce(ctx context.Context, handler transport.Handler) error {
	url, err := c.openConnection(ctx)
	if err != nil {
		return err
	}
	conn, err := c.cfg.DialWebSocket(ctx, url)
	if err != nil {
		return fmt.Errorf("slack: dial: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close(websocket.StatusNormalClosure, "loop exit")
			c.conn = nil
		}
	}()

	return c.pump(ctx, conn, handler)
}

// openConnection asks Slack for a fresh Socket Mode URL. The returned
// URL is single-use — reconnect goes through this endpoint again.
func (c *Client) openConnection(ctx context.Context) (string, error) {
	var resp struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := c.post(ctx, "apps.connections.open", c.cfg.AppToken, nil, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("slack: apps.connections.open: %s", resp.Error)
	}
	return resp.URL, nil
}

// pump reads events from the Socket Mode WebSocket, acks them, and
// forwards message-shaped events to the handler.
func (c *Client) pump(ctx context.Context, conn WSConn, handler transport.Handler) error {
	for {
		raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if err := c.handleFrame(ctx, conn, raw, handler); err != nil {
			c.logger.Warn("slack.frame_failed", slog.String("err", err.Error()))
		}
	}
}

// handleFrame dispatches a single Socket Mode envelope.
func (c *Client) handleFrame(ctx context.Context, conn WSConn, raw []byte, handler transport.Handler) error {
	var env socketEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("parse envelope: %w", err)
	}
	switch env.Type {
	case "hello", "disconnect":
		return nil
	case "events_api":
		if env.EnvelopeID != "" {
			ack := ackEnvelope{EnvelopeID: env.EnvelopeID}
			if b, err := json.Marshal(ack); err == nil {
				_ = conn.Write(ctx, b)
			}
		}
		return c.dispatchEvent(ctx, env, handler)
	default:
		// interactive / slash_commands ack silently; not the shape we
		// route through the agent handler.
		if env.EnvelopeID != "" {
			ack := ackEnvelope{EnvelopeID: env.EnvelopeID}
			if b, err := json.Marshal(ack); err == nil {
				_ = conn.Write(ctx, b)
			}
		}
		return nil
	}
}

// dispatchEvent extracts a message from an events_api envelope and
// forwards it to the handler.
func (c *Client) dispatchEvent(ctx context.Context, env socketEnvelope, handler transport.Handler) error {
	var payload eventsAPIPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return fmt.Errorf("parse payload: %w", err)
	}
	if payload.Event.Type != "message" || payload.Event.SubType != "" {
		return nil // ignore bot messages, edits, joins, etc.
	}
	// Loop prevention: own bot message.
	if c.cfg.BotUserID != "" && payload.Event.User == c.cfg.BotUserID {
		return nil
	}
	if payload.Event.BotID != "" {
		return nil
	}
	body := payload.Event.Text
	if body == "" {
		return nil
	}
	msg := transport.IncomingMessage{
		From: payload.Event.User,
		Body: body,
		At:   time.Now().UTC(),
	}
	c.logger.Info("slack.incoming",
		slog.String("from", msg.From),
		slog.String("channel", payload.Event.Channel))
	reply, err := handler.Handle(ctx, msg)
	if err != nil {
		c.logger.Error("slack.handler_failed", slog.String("err", err.Error()))
		return nil
	}
	if reply == "" {
		return nil
	}
	return c.postMessage(ctx, payload.Event.Channel, reply)
}

// Deliver sends a plain text message to a Slack channel id. Suitable
// as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, channelID, body string) error {
	return c.postMessage(ctx, channelID, body)
}

func (c *Client) postMessage(ctx context.Context, channel, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{"channel": channel, "text": body}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post(ctx, "chat.postMessage", c.cfg.BotToken, payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("slack: chat.postMessage: %s", resp.Error)
	}
	return nil
}

// post is the generic JSON POST helper. token selects which bearer
// header to use (app-level vs bot).
func (c *Client) post(ctx context.Context, method, token string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("slack: marshal %s: %w", method, err)
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader([]byte{})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/"+method, body)
	if err != nil {
		return fmt.Errorf("slack: build %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("slack: %s: read: %w", method, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack: %s: HTTP %d: %s", method, resp.StatusCode, truncate(string(rb), 400))
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(rb, result)
}

// -- default WebSocket dialer -----------------------------------------

func defaultDial(ctx context.Context, url string) (WSConn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return &wsConnAdapter{conn: conn}, nil
}

type wsConnAdapter struct{ conn *websocket.Conn }

func (a *wsConnAdapter) Read(ctx context.Context) ([]byte, error) {
	_, b, err := a.conn.Read(ctx)
	return b, err
}
func (a *wsConnAdapter) Write(ctx context.Context, msg []byte) error {
	return a.conn.Write(ctx, websocket.MessageText, msg)
}
func (a *wsConnAdapter) Close(code websocket.StatusCode, reason string) error {
	return a.conn.Close(code, reason)
}

// -- wire types --------------------------------------------------------

// socketEnvelope is the Socket Mode outer frame. Every inbound event
// arrives wrapped in this.
type socketEnvelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// ackEnvelope is what we send back to acknowledge an event.
type ackEnvelope struct {
	EnvelopeID string `json:"envelope_id"`
}

// eventsAPIPayload is the shape of the payload field for
// type=events_api envelopes.
type eventsAPIPayload struct {
	Event slackEvent `json:"event"`
}

type slackEvent struct {
	Type    string `json:"type"`
	SubType string `json:"subtype,omitempty"`
	User    string `json:"user,omitempty"`
	BotID   string `json:"bot_id,omitempty"`
	Text    string `json:"text,omitempty"`
	Channel string `json:"channel,omitempty"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
