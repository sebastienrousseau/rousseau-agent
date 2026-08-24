// Package telegram implements transport.Transport against the
// Telegram Bot HTTP API using long-polling.
//
// No third-party SDK — the API is a small handful of endpoints and
// pulling in a bot library would triple this package's dependency
// footprint. The Client speaks JSON directly.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the Telegram transport.
type Config struct {
	// Token is the bot token from BotFather. Required.
	Token string
	// BaseURL overrides the API endpoint. Empty defaults to
	// https://api.telegram.org. Useful for local Telegram Bot API
	// servers.
	BaseURL string
	// ReplyHeader is prepended to every outbound reply.
	ReplyHeader string
	// PollTimeout controls the long-poll timeout on getUpdates.
	// Zero uses 30s.
	PollTimeout time.Duration
	// HTTPClient overrides the default *http.Client. Zero uses a
	// client with a 60s per-request timeout.
	HTTPClient *http.Client
	// Transcriber, when non-nil, converts inbound voice / audio
	// messages to text before the router sees them. Nil skips audio
	// messages entirely (they don't fall through to the LLM).
	Transcriber Transcriber
}

// Transcriber matches the shape used by every rousseau transport that
// consumes audio; a nil transcriber tells the client to skip audio
// messages instead of erroring. See internal/media/audio for the
// backends and internal/media/audio.NewTranscriberString for the
// adapter that satisfies this interface from a generic audio.Backend.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimetype string) (string, error)
}

// Client is a transport.Transport backed by the Telegram Bot API.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	http    *http.Client
	stopped atomic.Bool
	mu      sync.Mutex
	offset  int64
}

// New constructs a Client. Token is required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("telegram: Token is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.telegram.org"
	}
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "telegram" }

// Start begins long-polling for updates and pumps them to handler.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("telegram: handler is required")
	}
	c.logger.Info("telegram.started")
	for {
		if c.stopped.Load() || ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := c.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logger.Warn("telegram.poll_failed", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for _, u := range updates {
			c.route(ctx, u, handler)
		}
	}
}

// Stop halts long-polling on the next iteration.
func (c *Client) Stop() error {
	c.stopped.Store(true)
	return nil
}

// Deliver sends a plain text message to the given chat id (as a
// string). Suitable as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, chat, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	chatID, err := strconv.ParseInt(chat, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: parse chat id %q: %w", chat, err)
	}
	payload := map[string]any{"chat_id": chatID, "text": body}
	return c.call(ctx, "sendMessage", payload, nil)
}

// route decides whether to invoke the handler for an update.
func (c *Client) route(ctx context.Context, u telegramUpdate, handler transport.Handler) {
	if u.Message == nil {
		return
	}

	// Resolve the message text. Prefer the explicit Text field; fall
	// back to transcribing an inbound voice / audio blob when the
	// caller wired a Transcriber. Skipping when neither is available.
	text := u.Message.Text
	if text == "" {
		text = c.transcribeAudio(ctx, u.Message)
	}
	if text == "" {
		return
	}

	msg := transport.IncomingMessage{
		From: strconv.FormatInt(u.Message.Chat.ID, 10),
		Body: text,
		At:   time.Unix(u.Message.Date, 0),
	}
	c.logger.Info("telegram.incoming", slog.String("from", msg.From))
	reply, err := handler.Handle(ctx, msg)
	if err != nil {
		c.logger.Error("telegram.handler_failed", slog.String("err", err.Error()))
		return
	}
	if reply == "" {
		return
	}
	if err := c.Deliver(ctx, msg.From, reply); err != nil {
		c.logger.Error("telegram.send_failed", slog.String("err", err.Error()))
	}
}

// transcribeAudio downloads the message's voice or audio blob and
// returns the transcript, or "" when there's no audio, no transcriber
// wired, or the download / transcription failed (in which case the
// error is logged and the message is silently skipped).
func (c *Client) transcribeAudio(ctx context.Context, m *telegramMessage) string {
	if c.cfg.Transcriber == nil {
		return ""
	}
	// Voice notes ship as `voice`; music / other audio as `audio`.
	// Both carry a file_id we can fetch via getFile → downloadFile.
	var (
		fileID   string
		mimeType string
	)
	switch {
	case m.Voice != nil:
		fileID = m.Voice.FileID
		mimeType = m.Voice.MimeType
	case m.Audio != nil:
		fileID = m.Audio.FileID
		mimeType = m.Audio.MimeType
	default:
		return ""
	}
	if fileID == "" {
		return ""
	}
	bytes, actualMime, err := c.downloadFile(ctx, fileID)
	if err != nil {
		c.logger.Warn("telegram.audio_download_failed", slog.String("err", err.Error()))
		return ""
	}
	if mimeType == "" {
		mimeType = actualMime
	}
	if mimeType == "" {
		// Telegram voice notes are Opus in an OGG container.
		mimeType = "audio/ogg"
	}
	text, err := c.cfg.Transcriber.Transcribe(ctx, bytes, mimeType)
	if err != nil {
		c.logger.Warn("telegram.transcribe_failed",
			slog.String("mimetype", mimeType),
			slog.Int("bytes", len(bytes)),
			slog.String("err", err.Error()))
		return ""
	}
	c.logger.Info("telegram.audio_transcribed",
		slog.String("mimetype", mimeType),
		slog.Int("bytes", len(bytes)),
		slog.Int("transcript_chars", len(text)))
	return text
}

// downloadFile calls getFile to learn the file_path, then GETs it from
// the /file/bot<token>/<path> endpoint. Returns (bytes, contentType).
func (c *Client) downloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	var out struct {
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &out); err != nil {
		return nil, "", fmt.Errorf("getFile: %w", err)
	}
	if out.Result.FilePath == "" {
		return nil, "", fmt.Errorf("getFile: empty file_path")
	}
	// The download endpoint differs from the bot API endpoint —
	// /file/bot<token>/<file_path> instead of /bot<token>/<method>.
	fileURL := c.cfg.BaseURL + "/file/bot" + url.PathEscape(c.cfg.Token) + "/" + out.Result.FilePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	// Cap the read to prevent a hostile / misconfigured file server
	// from streaming gigabytes into the daemon's memory.
	const maxDownload = 32 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxDownload {
		return nil, "", fmt.Errorf("download exceeds %d bytes", maxDownload)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// getUpdates issues a long-poll getUpdates call.
func (c *Client) getUpdates(ctx context.Context) ([]telegramUpdate, error) {
	c.mu.Lock()
	offset := c.offset
	c.mu.Unlock()

	payload := map[string]any{
		"timeout": int64(c.cfg.PollTimeout.Seconds()),
	}
	if offset > 0 {
		payload["offset"] = offset
	}
	var out struct {
		Result []telegramUpdate `json:"result"`
	}
	if err := c.call(ctx, "getUpdates", payload, &out); err != nil {
		return nil, err
	}
	if len(out.Result) > 0 {
		c.mu.Lock()
		c.offset = out.Result[len(out.Result)-1].UpdateID + 1
		c.mu.Unlock()
	}
	return out.Result, nil
}

// call is the generic Bot API JSON POST. When result is nil the
// caller does not care about the response body.
func (c *Client) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal %s: %w", method, err)
	}
	endpoint := c.cfg.BaseURL + "/bot" + url.PathEscape(c.cfg.Token) + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: %s: read body: %w", method, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram: %s: HTTP %d: %s", method, resp.StatusCode, truncate(string(rb), 400))
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(rb, result); err != nil {
		return fmt.Errorf("telegram: %s: parse response: %w", method, err)
	}
	return nil
}

// telegramUpdate / telegramMessage / telegramChat are the subset of
// the Bot API objects we consume.
type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	MessageID int64          `json:"message_id"`
	Date      int64          `json:"date"`
	Text      string         `json:"text"`
	Chat      telegramChat   `json:"chat"`
	Voice     *telegramVoice `json:"voice,omitempty"`
	Audio     *telegramAudio `json:"audio,omitempty"`
}

// telegramVoice is the Bot API's Voice object — Opus in an OGG
// container. Duration is documented in seconds.
type telegramVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
}

// telegramAudio covers non-voice audio uploads (mp3 / m4a / …).
type telegramAudio struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
	Title    string `json:"title"`
}

type telegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Username string `json:"username"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
