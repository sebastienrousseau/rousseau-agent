package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

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
			_ = c.conn.Close(websocket.StatusNormalClosure, "session end") //nolint:errcheck // best-effort close
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
		// The turn runs off the gateway loop so other senders (and
		// their /cancel) keep being read and heartbeats keep flowing.
		c.inflight.Do(func() {
			if err := c.handleMessage(ctx, m, handler); err != nil {
				c.logger.Error("discord.send_failed", slog.String("err", err.Error()))
			}
		})
		return nil
	default:
		return nil
	}
}

// handleMessage turns one MESSAGE_CREATE into an IncomingMessage, runs
// the handler, and posts the reply to the channel.
func (c *Client) handleMessage(ctx context.Context, m discordMessage, handler transport.Handler) error {
	body := m.Content
	mediaOK := c.cfg.IsAllowed == nil || c.cfg.IsAllowed(m.Author.ID)
	if body == "" && mediaOK {
		body = c.transcribeAudio(ctx, &m)
	}
	var attachments []transport.Attachment
	if mediaOK {
		attachments = c.collectImageAttachments(ctx, m.Attachments)
	}
	if body == "" && len(attachments) == 0 {
		return nil
	}
	msg := transport.IncomingMessage{
		From:         m.Author.ID,
		Body:         body,
		At:           time.Now().UTC(),
		Conversation: m.ChannelID,
		MessageID:    m.ID,
		IsDirect:     m.GuildID == "", // a DM channel has no guild
		Attachments:  attachments,
	}
	c.logger.Info("discord.incoming",
		slog.String("from", msg.From),
		slog.String("channel", m.ChannelID),
		slog.Int("attachments", len(attachments)))
	reply, err := handler.Handle(ctx, msg)
	if err != nil {
		c.logger.Error("discord.handler_failed", slog.String("err", err.Error()))
		return nil
	}
	for _, part := range transport.SplitReply(reply, maxTextLen) {
		if err := c.postMessage(ctx, m.ChannelID, part); err != nil {
			return err
		}
	}
	return nil
}

// -- default WebSocket dialer -----------------------------------------

func defaultDial(ctx context.Context, url string) (WSConn, error) {
	conn, resp, err := websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
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
	ID          string              `json:"id"`
	ChannelID   string              `json:"channel_id"`
	GuildID     string              `json:"guild_id,omitempty"`
	Author      discordUser         `json:"author"`
	Content     string              `json:"content"`
	Attachments []discordAttachment `json:"attachments,omitempty"`
}

// discordAttachment is the subset of Discord's attachment object we
// need for audio transcription (id, url, content_type, size).
type discordAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int    `json:"size"`
	URL         string `json:"url"`
	ProxyURL    string `json:"proxy_url"`
	ContentType string `json:"content_type,omitempty"`
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot,omitempty"`
}
