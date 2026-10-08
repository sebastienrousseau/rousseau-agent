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

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
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
	// MediaPolicy governs which files are accepted (MIME allowlist,
	// per-image and per-turn byte caps). Zero-value falls back to the
	// media.Policy defaults documented on that package.
	MediaPolicy media.Policy
	// IsAllowed, when set, gates media pre-processing: for a sender it
	// rejects, voice notes are not transcribed and files are not
	// downloaded, so a stranger cannot spend bandwidth, CPU or API
	// budget. Text still reaches the router, which makes the final
	// decision (and handles SSO /login). Wired to Router.Allowed.
	IsAllowed func(from string) bool
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

	// inflight runs each event off the read loop (see
	// transport.Inflight); nil (inline) outside Start.
	inflight *transport.Inflight

	// files carries the bot token to file downloads; see files.go.
	files fileFetcher
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
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient,
		files: newFileFetcher(cfg.HTTPClient)}, nil
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
	c.inflight = new(transport.Inflight)
	defer c.inflight.Wait()
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
		_ = c.conn.Close(websocket.StatusNormalClosure, "shutdown") //nolint:errcheck // best-effort close
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
			_ = c.conn.Close(websocket.StatusNormalClosure, "loop exit") //nolint:errcheck // best-effort close
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
				_ = conn.Write(ctx, b) //nolint:errcheck // best-effort ack
			}
		}
		var payload eventsAPIPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return fmt.Errorf("parse payload: %w", err)
		}
		// Acked above; the agent turn runs off the read loop so other
		// senders (and their /cancel) keep being read.
		c.inflight.Do(func() {
			if err := c.dispatchEvent(ctx, payload, handler); err != nil {
				c.logger.Warn("slack.frame_failed", slog.String("err", err.Error()))
			}
		})
		return nil
	default:
		// interactive / slash_commands ack silently; not the shape we
		// route through the agent handler.
		if env.EnvelopeID != "" {
			ack := ackEnvelope{EnvelopeID: env.EnvelopeID}
			if b, err := json.Marshal(ack); err == nil {
				_ = conn.Write(ctx, b) //nolint:errcheck // best-effort ack
			}
		}
		return nil
	}
}

// dispatchEvent extracts a message from a parsed events_api payload and
// forwards it to the handler.
func (c *Client) dispatchEvent(ctx context.Context, payload eventsAPIPayload, handler transport.Handler) error {
	if c.skipEvent(payload.Event) {
		return nil
	}
	body := payload.Event.Text
	var attachments []transport.Attachment
	if c.cfg.IsAllowed == nil || c.cfg.IsAllowed(payload.Event.User) {
		attachments = c.collectFileAttachments(ctx, payload.Event.Files)
	}
	if body == "" && len(attachments) == 0 {
		return nil
	}
	msg := transport.IncomingMessage{
		From:         payload.Event.User,
		Body:         body,
		At:           time.Now().UTC(),
		Conversation: payload.Event.Channel,
		MessageID:    payload.Event.TS,
		IsDirect:     payload.Event.ChannelType == "im",
		Thread:       payload.Event.ThreadTS,
		Attachments:  attachments,
	}
	c.logger.Info("slack.incoming",
		slog.String("from", msg.From),
		slog.String("channel", payload.Event.Channel),
		slog.Int("attachments", len(attachments)))
	reply, err := handler.Handle(ctx, msg)
	if err != nil {
		c.logger.Error("slack.handler_failed", slog.String("err", err.Error()))
		return nil
	}
	// A message in a thread is answered in that thread; a top-level
	// message gets a top-level reply, like a human colleague.
	return c.replyIn(ctx, payload.Event.Channel, payload.Event.ThreadTS, reply)
}

// skipEvent reports whether an events_api message is not a user
// message the handler should see.
func (c *Client) skipEvent(e slackEvent) bool {
	if e.Type != "message" {
		return true
	}
	// Slack tags plain user attachments with subtype="file_share";
	// bot edits, joins, and channel-events use other subtypes we do
	// not want. Empty-subtype and file_share are the two shapes real
	// user messages take.
	if e.SubType != "" && e.SubType != "file_share" {
		return true
	}
	// Loop prevention: own bot message, or any bot.
	if c.cfg.BotUserID != "" && e.User == c.cfg.BotUserID {
		return true
	}
	return e.BotID != ""
}

// replyIn posts reply to channel (inside threadTS when set), split to
// the platform cap; an empty reply posts nothing.
func (c *Client) replyIn(ctx context.Context, channel, threadTS, reply string) error {
	for _, part := range transport.SplitReply(reply, maxTextLen) {
		if err := c.postMessageIn(ctx, channel, threadTS, part); err != nil {
			return err
		}
	}
	return nil
}

// maxTextLen is Slack's documented ceiling for chat.postMessage text
// before it is truncated; longer replies go out as several messages.
const maxTextLen = 4000

// collectFileAttachments walks the files array on an inbound event,
// downloads each with the bot token, and applies the operator's
// MediaPolicy. Failures per file are logged and dropped rather than
// aborting the whole message — one bad file must not swallow a whole
// user message. Non-image files are ignored entirely: the model has
// no adapter to accept PDF/video today, so downloading them would
// only waste egress.
func (c *Client) collectFileAttachments(ctx context.Context, files []slackFile) []transport.Attachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]transport.Attachment, 0, len(files))
	totalSoFar := 0
	for _, f := range files {
		if f.URLPrivateDownload == "" {
			continue
		}
		if !isImageMIME(f.Mimetype) {
			c.logger.Debug("slack.file_skipped_non_image",
				slog.String("file", f.ID),
				slog.String("envelope_mime", f.Mimetype))
			continue
		}
		data, err := c.downloadFile(ctx, f.URLPrivateDownload)
		if err != nil {
			c.logger.Warn("slack.file_download_failed",
				slog.String("file", f.ID),
				slog.String("err", err.Error()))
			continue
		}
		sniffed, err := c.cfg.MediaPolicy.Accept(data, totalSoFar)
		if err != nil {
			c.logger.Info("slack.file_dropped",
				slog.String("file", f.ID),
				slog.String("reason", err.Error()),
				slog.Int("bytes", len(data)))
			continue
		}
		if sniffed != f.Mimetype {
			c.logger.Warn("slack.file_mime_lied",
				slog.String("file", f.ID),
				slog.String("envelope", f.Mimetype),
				slog.String("sniffed", sniffed))
		}
		out = append(out, transport.Attachment{MediaType: sniffed, Data: data})
		totalSoFar += len(data)
	}
	return out
}

// isImageMIME returns true when the envelope MIME is one we intend
// to attempt to accept. Kept separate from the media.Policy check
// so the "PDF, skip" path never even opens the connection — Slack
// file downloads are authenticated and metered.
func isImageMIME(m string) bool {
	if len(m) < 6 {
		return false
	}
	return m[:6] == "image/"
}

// Deliver sends a plain text message to a Slack channel id. Suitable
// as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, channelID, body string) error {
	return c.postMessage(ctx, channelID, body)
}

func (c *Client) postMessage(ctx context.Context, channel, body string) error {
	return c.postMessageIn(ctx, channel, "", body)
}

// postMessageIn posts body to channel, inside the thread threadTS
// when it is non-empty.
func (c *Client) postMessageIn(ctx context.Context, channel, threadTS, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{"channel": channel, "text": body}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
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
	rb, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
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
	// ChannelType is "im" for a direct message with the bot, and
	// "channel", "group" or "mpim" for conversations others read.
	ChannelType string `json:"channel_type,omitempty"`
	// TS is the message's id within the channel; ThreadTS is set on
	// a message posted inside a thread and names the thread's root.
	TS       string      `json:"ts,omitempty"`
	ThreadTS string      `json:"thread_ts,omitempty"`
	Files    []slackFile `json:"files,omitempty"`
}

// slackFile is the subset of the Slack file object we need. The full
// object is documented at https://api.slack.com/types/file — every
// other field is metadata we do not need.
type slackFile struct {
	ID                 string `json:"id"`
	Mimetype           string `json:"mimetype"`
	Size               int    `json:"size"`
	URLPrivateDownload string `json:"url_private_download"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
