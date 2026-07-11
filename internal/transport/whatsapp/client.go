// Package whatsapp implements transport.Transport on top of
// go.mau.fi/whatsmeow (reverse-engineered WhatsApp Web multi-device
// client).
//
// This uses the UNOFFICIAL WhatsApp protocol. Meta occasionally bans
// numbers that use unofficial clients. Do not use this on a personal
// number you rely on for anything important.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	_ "modernc.org/sqlite" // register the modernc SQLite driver used by whatsmeow

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the WhatsApp transport.
type Config struct {
	// StoreDSN is the SQLite DSN whatsmeow will use for device
	// credentials. Kept separate from the agent's session store so a
	// device relink does not touch conversation history.
	StoreDSN string
	// LogLevel is passed to whatsmeow's logger ("DEBUG", "INFO", "WARN").
	// Empty defaults to "WARN".
	LogLevel string
}

// Client is a transport.Transport backed by whatsmeow.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	mu      sync.Mutex
	wm      *whatsmeow.Client
	handler transport.Handler
	stopped bool
}

// New constructs a Client. Connect is deferred until Start.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.StoreDSN == "" {
		return nil, errors.New("whatsapp: StoreDSN is required")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "WARN"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "whatsapp" }

// Start connects to WhatsApp Web (printing a QR to stdout on first
// pairing) and pumps messages to handler until ctx is cancelled or Stop
// is called.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	c.mu.Lock()
	if c.wm != nil {
		c.mu.Unlock()
		return errors.New("whatsapp: already started")
	}
	c.handler = handler
	c.mu.Unlock()

	dbLog := waLog.Stdout("wa-db", c.cfg.LogLevel, true)
	container, err := sqlstore.New(ctx, "sqlite", c.cfg.StoreDSN, dbLog)
	if err != nil {
		return fmt.Errorf("whatsapp: open store: %w", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: get device: %w", err)
	}

	clientLog := waLog.Stdout("wa", c.cfg.LogLevel, true)
	wm := whatsmeow.NewClient(device, clientLog)
	wm.AddEventHandler(c.onEvent)

	c.mu.Lock()
	c.wm = wm
	c.mu.Unlock()

	if wm.Store.ID == nil {
		qrChan, err := wm.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("whatsapp: qr channel: %w", err)
		}
		if err := wm.Connect(); err != nil {
			return fmt.Errorf("whatsapp: connect: %w", err)
		}
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				c.logger.Info("whatsapp.qr_ready")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			case "success":
				c.logger.Info("whatsapp.paired")
			default:
				c.logger.Warn("whatsapp.qr_event", slog.String("event", evt.Event))
			}
		}
	} else if err := wm.Connect(); err != nil {
		return fmt.Errorf("whatsapp: connect: %w", err)
	}

	<-ctx.Done()
	return c.Stop()
}

// Stop disconnects the whatsmeow client. Safe to call multiple times.
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil
	}
	c.stopped = true
	if c.wm != nil {
		c.wm.Disconnect()
	}
	return nil
}

func (c *Client) onEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Message:
		c.handleMessage(evt)
	case *events.Connected:
		c.logger.Info("whatsapp.connected")
	case *events.Disconnected:
		c.logger.Warn("whatsapp.disconnected")
	case *events.LoggedOut:
		c.logger.Error("whatsapp.logged_out", slog.Int("reason", int(evt.Reason)))
	}
}

func (c *Client) handleMessage(evt *events.Message) {
	if evt.Info.IsGroup {
		return
	}
	// Loop-prevention: skip messages this exact linked device sent (our
	// own replies echoing back). Messages from the account's *other*
	// linked devices — the primary phone testing via "message yourself"
	// — carry IsFromMe=true too, and must still be processed.
	c.mu.Lock()
	wm := c.wm
	c.mu.Unlock()
	if evt.Info.IsFromMe && wm != nil && wm.Store.ID != nil &&
		evt.Info.Sender.Device == wm.Store.ID.Device {
		return
	}
	body := strings.TrimSpace(extractText(evt.Message))
	if body == "" {
		return
	}

	// Determine the "From" the router sees. Two normalisations:
	//
	//  1. Strip the multi-device address suffix (":<n>") so allowlists
	//     written as the plain user JID match regardless of which
	//     linked device sent the message.
	//
	//  2. For the account holder's own outbound (IsFromMe=true from a
	//     linked device that isn't us), WhatsApp reports the sender as
	//     the account's LID (e.g. "276540210315282@lid") rather than
	//     the phone JID. That's a privacy feature of the multi-device
	//     protocol. Substitute our own account JID so operators can
	//     allowlist "447906009073@s.whatsapp.net" and have
	//     "message yourself" testing route correctly.
	from := evt.Info.Sender.ToNonAD()
	if evt.Info.IsFromMe && wm.Store.ID != nil {
		from = wm.Store.ID.ToNonAD()
	}
	msg := transport.IncomingMessage{
		From: from.String(),
		Body: body,
		At:   evt.Info.Timestamp,
	}

	ctx := context.Background()
	c.logger.Info("whatsapp.incoming", slog.String("from", msg.From))

	start := time.Now()
	reply, err := c.handler.Handle(ctx, msg)
	elapsed := time.Since(start)
	if err != nil {
		c.logger.Error("whatsapp.handler_failed",
			slog.String("err", err.Error()),
			slog.Duration("elapsed", elapsed))
		return
	}
	if reply == "" {
		c.logger.Info("whatsapp.empty_reply", slog.Duration("elapsed", elapsed))
		return
	}
	c.logger.Info("whatsapp.handler_ok",
		slog.Duration("elapsed", elapsed),
		slog.Int("reply_len", len(reply)))
	if err := c.send(ctx, evt.Info.Chat, reply); err != nil {
		c.logger.Error("whatsapp.send_failed", slog.String("err", err.Error()))
	}
}

func (c *Client) send(ctx context.Context, to types.JID, text string) error {
	c.mu.Lock()
	wm := c.wm
	c.mu.Unlock()
	if wm == nil {
		return errors.New("whatsapp: not connected")
	}
	_, err := wm.SendMessage(ctx, to, &waProto.Message{
		Conversation: proto.String(text),
	})
	return err
}

func extractText(m *waProto.Message) string {
	if m == nil {
		return ""
	}
	if v := m.GetConversation(); v != "" {
		return v
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
