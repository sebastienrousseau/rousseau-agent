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
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Gateway intent bits — only the ones we need.
const (
	intentGuildMessages  = 1 << 9
	intentDirectMessages = 1 << 12
	intentMessageContent = 1 << 15
)

// Transcriber turns an audio blob (voice message / audio attachment)
// into text. Empty return means "give up silently" — the transport
// then just drops the message rather than replying with a stub. Wired
// from media.audio.backend via the CLI. Nil disables audio handling.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimetype string) (string, error)
}

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
	// Transcriber is invoked when a message has no text but carries an
	// audio attachment (content_type starts with "audio/"). Nil means
	// audio attachments are silently ignored.
	Transcriber Transcriber
	// IsAllowed, when set, gates media pre-processing: for a sender it
	// rejects, voice notes are not transcribed and files are not
	// downloaded, so a stranger cannot spend bandwidth, CPU or API
	// budget. Text still reaches the router, which makes the final
	// decision (and handles SSO /login). Wired to Router.Allowed.
	IsAllowed func(from string) bool
	// MaxAudioBytes caps per-attachment downloads to protect the
	// process from a maliciously large file. Zero uses 32 MiB.
	MaxAudioBytes int64
	// MediaPolicy governs which image attachments are accepted (MIME
	// allowlist, per-image and per-turn byte caps). Zero-value falls
	// back to the media.Policy defaults documented on that package.
	MediaPolicy media.Policy
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

	// inflight runs each message off the gateway loop (see
	// transport.Inflight); nil (inline) outside Start.
	inflight *transport.Inflight
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
		_ = c.conn.Close(websocket.StatusNormalClosure, "shutdown") //nolint:errcheck // best-effort close
		c.conn = nil
	}
	return nil
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
