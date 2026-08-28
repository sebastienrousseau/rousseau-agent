//go:build !no_whatsmeow

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
	"io"
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

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config and DefaultReplyHeader are defined in types.go (tag-free) so
// cli/whatsapp.go compiles under both the default whatsmeow build and
// the no_whatsmeow lite variant.

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
	bus        *progress.Bus
	mu         sync.Mutex
	wm         *whatsmeow.Client
	sender     Sender
	downloader Downloader
	ownID      *types.JID
	handler    transport.Handler
	stopped    bool
	// dispatch decides whether an inbound message is handled inline or
	// on its own goroutine.
	//
	// whatsmeow drains its inbound node queue on ONE goroutine and
	// dispatches message events synchronously, so a handler that
	// blocks for the length of an agent turn also blocks every message
	// that arrives during it — which would make mid-flight interaction
	// impossible by construction. Start therefore switches this to
	// "go f()". It stays inline by default so unit tests that build a
	// Client directly keep their synchronous, race-free assertions.
	dispatch func(func())
}

// Start-path test seams. Start's whatsmeow touchpoints — opening the
// device store, requesting the pairing QR channel, dialling the
// websocket, and rendering the QR — are held in package-level vars so
// unit tests can drive the pairing and error branches without a
// socket. Production values are the real whatsmeow calls; only tests
// reassign them.
var (
	newWMClient = func(ctx context.Context, cfg Config) (*whatsmeow.Client, error) {
		dbLog := waLog.Stdout("wa-db", cfg.LogLevel, true)
		container, err := sqlstore.New(ctx, "sqlite", cfg.StoreDSN, dbLog)
		if err != nil {
			return nil, fmt.Errorf("whatsapp: open store: %w", err)
		}
		device, err := container.GetFirstDevice(ctx)
		if err != nil {
			return nil, fmt.Errorf("whatsapp: get device: %w", err)
		}
		clientLog := waLog.Stdout("wa", cfg.LogLevel, true)
		return whatsmeow.NewClient(device, clientLog), nil
	}

	wmQRChannel = func(ctx context.Context, wm *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
		return wm.GetQRChannel(ctx)
	}

	wmConnect = func(wm *whatsmeow.Client) error { return wm.Connect() }

	qrOut io.Writer = os.Stdout
)

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
	return &Client{
		cfg:      cfg,
		logger:   logger,
		bus:      progress.NewBus(progress.BusOptions{}),
		dispatch: func(f func()) { f() },
	}, nil
}

// Bus returns the progress bus this transport delivers live updates
// from. Daemon assembly hands it to the control registry so the agent
// loop's events reach this transport's reporters.
func (c *Client) Bus() *progress.Bus { return c.bus }

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

	wm, err := newWMClient(ctx, c.cfg)
	if err != nil {
		return err
	}
	// From here on inbound messages are handled off the whatsmeow
	// receive goroutine, so a running turn no longer stalls delivery
	// of the next message.
	c.mu.Lock()
	c.dispatch = func(f func()) { go f() }
	c.mu.Unlock()
	wm.AddEventHandler(c.onEvent)

	c.mu.Lock()
	c.wm = wm
	c.sender = newWMSender(wm)
	c.downloader = newWMDownloader(wm)
	c.ownID = wm.Store.ID
	c.mu.Unlock()

	if wm.Store.ID == nil {
		qrChan, err := wmQRChannel(ctx, wm)
		if err != nil {
			return fmt.Errorf("whatsapp: qr channel: %w", err)
		}
		if err := wmConnect(wm); err != nil {
			return fmt.Errorf("whatsapp: connect: %w", err)
		}
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				c.logger.Info("whatsapp.qr_ready")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, qrOut)
			case "success":
				c.logger.Info("whatsapp.paired")
			default:
				c.logger.Warn("whatsapp.qr_event", slog.String("event", evt.Event))
			}
		}
	} else if err := wmConnect(wm); err != nil {
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
	c.bus.Close()
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
	sender, downloader, ownID, dispatch := c.sender, c.downloader, c.ownID, c.dispatch
	c.mu.Unlock()
	if sender == nil {
		return
	}
	dispatch(func() {
		c.dispatchOne(evt, sender, downloader, ownID)
	})
}

// dispatchOne runs one inbound message through Dispatch. Split out so
// handleMessage's goroutine seam stays a one-liner.
func (c *Client) dispatchOne(evt *events.Message, sender Sender, downloader Downloader, ownID *types.JID) {
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       ownID,
		Sender:      sender,
		Downloader:  downloader,
		Handler:     c.handler,
		Transcriber: c.cfg.Transcriber,
		Header:      c.cfg.ReplyHeader,
		Logger:      c.logger,
		Progress:    c.bus,
	})
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
