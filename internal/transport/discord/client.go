// Package discord implements transport.Transport against Discord's
// Gateway WebSocket + REST API.
//
// Gateway protocol: send Identify → receive Ready → keep-alive with
// Heartbeat + heartbeat_ack → receive Dispatch events (MESSAGE_CREATE).
// REST outbound via POST /channels/{id}/messages.
//
// Prerequisites the operator arranges out-of-band:
//   - A Discord Application with a Bot user
//   - Bot token
//   - Message Content intent enabled in the developer portal
//   - Bot invited to at least one server (or DMs enabled)
package discord

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

// Gateway intent bits — only the ones we need.
const (
	intentGuildMessages  = 1 << 9
	intentDirectMessages = 1 << 12
	intentMessageContent = 1 << 15
)

// Config configures the Discord transport.
type Config struct {
	// Token is the bot token (from the developer portal). Required.
	Token string
	// GatewayURL overrides the WSS gateway. Empty defaults to
	// "wss://gateway.discord.gg/?v=10&encoding=json".
	GatewayURL string
	// BaseURL overrides the REST endpoint. Empty defaults to
	// https://discord.com/api/v10.
	BaseURL string
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string
	// HTTPClient overrides the REST transport. Zero uses 30s timeout.
	HTTPClient *http.Client
	// DialWebSocket overrides the WebSocket dialer for tests.
	DialWebSocket func(ctx context.Context, url string) (WSConn, error)
}

// WSConn is the narrow subset of the WebSocket API the transport uses.
type WSConn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close(code websocket.StatusCode, reason string) error
}

// Client is a transport.Transport backed by Discord.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	http    *http.Client
	stopped atomic.Bool

	mu     sync.Mutex
	conn   WSConn
	selfID string
	seq    int64
}

// New constructs a Client. Token is required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("discord: Token is required")
	}
	if cfg.GatewayURL == "" {
		cfg.GatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://discord.com/api/v10"
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
func (*Client) Name() string { return "discord" }

// Start opens the Gateway and pumps events to handler until ctx is
// cancelled.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("discord: handler is required")
	}
	c.logger.Info("discord.started")
	for {
		if c.stopped.Load() || ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.runOnce(ctx, handler); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logger.Warn("discord.session_failed", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
	}
}

// Stop halts the loop.
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

// runOnce opens one Gateway session and pumps until an error.
func (c *Client) runOnce(ctx context.Context, handler transport.Handler) error {
	conn, err := c.cfg.DialWebSocket(ctx, c.cfg.GatewayURL)
	if err != nil {
		return fmt.Errorf("discord: dial: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close(websocket.StatusNormalClosure, "session end")
			c.conn = nil
		}
	}()

	// Identify happens after Hello. We handle both from the same pump.
	return c.pump(ctx, conn, handler)
}

// pump reads events off the Gateway and dispatches them.
func (c *Client) pump(ctx context.Context, conn WSConn, handler transport.Handler) error {
	for {
		raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if err := c.handleFrame(ctx, conn, raw, handler); err != nil {
			c.logger.Warn("discord.frame_failed", slog.String("err", err.Error()))
		}
	}
}

// handleFrame processes one Gateway frame.
func (c *Client) handleFrame(ctx context.Context, conn WSConn, raw []byte, handler transport.Handler) error {
	var frame gatewayFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return fmt.Errorf("parse frame: %w", err)
	}
	if frame.S != 0 {
		c.mu.Lock()
		c.seq = frame.S
		c.mu.Unlock()
	}
	switch frame.Op {
	case 10: // HELLO — identify.
		return c.sendIdentify(ctx, conn)
	case 11: // HEARTBEAT_ACK
		return nil
	case 0: // DISPATCH
		return c.dispatch(ctx, frame, handler)
	default:
		return nil // Reconnect (7), Invalid Session (9), etc. — treated as fatal by returning from pump.
	}
}

// sendIdentify writes the Identify (op 2) payload.
func (c *Client) sendIdentify(ctx context.Context, conn WSConn) error {
	identify := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   c.cfg.Token,
			"intents": intentGuildMessages | intentDirectMessages | intentMessageContent,
			"properties": map[string]string{
				"$os":      "linux",
				"$browser": "rousseau-agent",
				"$device":  "rousseau-agent",
			},
		},
	}
	b, err := json.Marshal(identify)
	if err != nil {
		return fmt.Errorf("identify marshal: %w", err)
	}
	return conn.Write(ctx, b)
}

// dispatch routes DISPATCH events. Only MESSAGE_CREATE from non-bots
// on a channel we can see reaches the handler.
func (c *Client) dispatch(ctx context.Context, frame gatewayFrame, handler transport.Handler) error {
	switch frame.T {
	case "READY":
		var ready readyPayload
		if err := json.Unmarshal(frame.D, &ready); err != nil {
			return fmt.Errorf("parse ready: %w", err)
		}
		c.mu.Lock()
		c.selfID = ready.User.ID
		c.mu.Unlock()
		c.logger.Info("discord.ready", slog.String("bot_id", ready.User.ID))
		return nil
	case "MESSAGE_CREATE":
		var m discordMessage
		if err := json.Unmarshal(frame.D, &m); err != nil {
			return fmt.Errorf("parse message: %w", err)
		}
		if m.Author.Bot {
			return nil
		}
		c.mu.Lock()
		selfID := c.selfID
		c.mu.Unlock()
		if selfID != "" && m.Author.ID == selfID {
			return nil // shouldn't happen (bot=true above) but belt+braces
		}
		if m.Content == "" {
			return nil
		}
		msg := transport.IncomingMessage{
			From: m.Author.ID,
			Body: m.Content,
			At:   time.Now().UTC(),
		}
		c.logger.Info("discord.incoming",
			slog.String("from", msg.From),
			slog.String("channel", m.ChannelID))
		reply, err := handler.Handle(ctx, msg)
		if err != nil {
			c.logger.Error("discord.handler_failed", slog.String("err", err.Error()))
			return nil
		}
		if reply == "" {
			return nil
		}
		return c.postMessage(ctx, m.ChannelID, reply)
	default:
		return nil
	}
}

// Deliver posts to a channel id. Suitable as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, channelID, body string) error {
	return c.postMessage(ctx, channelID, body)
}

func (c *Client) postMessage(ctx context.Context, channelID, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{"content": body}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/channels/%s/messages", c.cfg.BaseURL, channelID), bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("discord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+c.cfg.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("discord: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord: HTTP %d: %s", resp.StatusCode, truncate(string(rb), 400))
	}
	return nil
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

type gatewayFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type readyPayload struct {
	V         int         `json:"v"`
	User      discordUser `json:"user"`
	SessionID string      `json:"session_id"`
}

type discordMessage struct {
	ID        string      `json:"id"`
	ChannelID string      `json:"channel_id"`
	GuildID   string      `json:"guild_id,omitempty"`
	Author    discordUser `json:"author"`
	Content   string      `json:"content"`
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot,omitempty"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
