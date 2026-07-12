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
	"sync"

	"github.com/mdp/qrterminal/v3"
	_ "modernc.org/sqlite" // register the modernc SQLite driver used by whatsmeow

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// DefaultReplyHeader is prepended to every outbound reply so the sender
// is obvious on the phone side. WhatsApp renders `*text*` in bold.
const DefaultReplyHeader = "💎 *Rousseau Agent*\n\n"

// Config configures the WhatsApp transport.
type Config struct {
	// StoreDSN is the SQLite DSN whatsmeow will use for device
	// credentials. Kept separate from the agent's session store so a
	// device relink does not touch conversation history.
	StoreDSN string
	// LogLevel is passed to whatsmeow's logger ("DEBUG", "INFO", "WARN").
	// Empty defaults to "WARN".
	LogLevel string
	// ReplyHeader is prepended to every outbound reply. Empty uses
	// DefaultReplyHeader; explicitly setting a single space " " disables
	// the prefix.
	ReplyHeader string
	// Transcriber optionally handles voice-note messages. When nil,
	// audio messages are logged and skipped.
	Transcriber Transcriber
}

// Client is a transport.Transport backed by whatsmeow.
//
// The whatsmeow.Client is stored on wm; the concrete client's
// send/download surface is stored on sender/downloader as the small
// interfaces the rest of the package speaks. Populating those
// separately lets unit tests swap in fakes without instantiating a
// whatsmeow.Client or opening a real socket.
type Client struct {
	cfg        Config
	logger     *slog.Logger
	mu         sync.Mutex
	wm         *whatsmeow.Client
	sender     Sender
	downloader Downloader
	ownID      *types.JID
	handler    transport.Handler
	stopped    bool
}

// New constructs a Client. Connect is deferred until Start.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.StoreDSN == "" {
		return nil, errors.New("whatsapp: StoreDSN is required")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "WARN"
	}
	if cfg.ReplyHeader == "" {
		cfg.ReplyHeader = DefaultReplyHeader
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
	c.sender = newWMSender(wm)
	c.downloader = newWMDownloader(wm)
	c.ownID = wm.Store.ID
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

// Deliver sends a plain-text message to the given JID string. Suitable
// as a cron.Delivery target — the scheduler uses this to ship
// scheduled prompt results to a WhatsApp contact without importing
// this package's types directly.
func (c *Client) Deliver(ctx context.Context, target, body string) error {
	c.mu.Lock()
	sender := c.sender
	c.mu.Unlock()
	if sender == nil {
		return errors.New("whatsapp: not connected")
	}
	jid, err := parseJID(target)
	if err != nil {
		return err
	}
	return sender.SendText(ctx, jid, PrependHeader(body, c.cfg.ReplyHeader))
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
	c.mu.Lock()
	sender, downloader, ownID := c.sender, c.downloader, c.ownID
	c.mu.Unlock()
	if sender == nil {
		return
	}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       ownID,
		Sender:      sender,
		Downloader:  downloader,
		Handler:     c.handler,
		Transcriber: c.cfg.Transcriber,
		Header:      c.cfg.ReplyHeader,
		Logger:      c.logger,
	})
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
