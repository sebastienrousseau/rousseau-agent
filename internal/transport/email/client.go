// Package email implements transport.Transport over IMAP (inbound)
// and SMTP (outbound). Polls INBOX for UNSEEN messages, marks them
// SEEN after handoff to the handler, and replies via stdlib net/smtp.
//
// The transport interface's IMAP surface is small enough that we
// depend only on emersion's go-imap v2 for the wire protocol; the
// SMTP side uses the standard library.
package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the Email transport.
type Config struct {
	// IMAP settings — inbound.
	IMAPAddr     string // "imap.example.com:993"
	IMAPUsername string
	IMAPPassword string
	// Mailbox to poll. Empty defaults to "INBOX".
	Mailbox string
	// PollInterval controls how often we look for new UNSEEN mail.
	// Zero uses 30s. IMAP IDLE is left for a future upgrade.
	PollInterval time.Duration

	// SMTP settings — outbound.
	SMTPAddr     string // "smtp.example.com:587"
	SMTPUsername string
	SMTPPassword string

	// From is the envelope + header From address.
	From string
	// ReplyHeader is prepended to every outbound message body.
	ReplyHeader string
	// RequireAuthResults drops inbound mail unless the receiving
	// MTA's Authentication-Results header reports dkim=pass with a
	// signing domain matching the From address. Without it the
	// allow-list keys on a header anyone can forge.
	RequireAuthResults bool

	// IMAPClientFactory is optional test injection; nil uses
	// imapclient.DialTLS.
	IMAPClientFactory func(addr, user, pass string) (IMAPClient, error)
	// SendMail is the SMTP write path; nil uses net/smtp.SendMail.
	SendMail func(addr, from string, to []string, msg []byte, user, pass string) error
}

// IMAPClient is the narrow subset of imapclient.Client that the
// transport uses. Extracted so tests inject a fake without opening a
// real IMAP session.
type IMAPClient interface {
	Select(mailbox string, opts *imap.SelectOptions) (*imap.SelectData, error)
	Search(criteria *imap.SearchCriteria, opts *imap.SearchOptions) (*imap.SearchData, error)
	Fetch(seqSet imap.NumSet, options *imap.FetchOptions) FetchCommand
	Store(seqSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) StoreCommand
	Close() error
}

// FetchCommand mirrors the collect-and-close shape of imapclient's
// fetch command; extracted for the same testability reason.
type FetchCommand interface {
	Collect() ([]*imapclient.FetchMessageBuffer, error)
	Close() error
}

// StoreCommand mirrors imapclient.StoreCommand.
type StoreCommand interface {
	Close() error
}

// Client is a transport.Transport backed by IMAP + SMTP.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	stopped atomic.Bool

	// inflight runs each mail's turn off the poll loop (see
	// transport.Inflight); nil (inline) outside Start.
	inflight *transport.Inflight
}

// New constructs a Client.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.IMAPAddr == "" || cfg.IMAPUsername == "" || cfg.IMAPPassword == "" {
		return nil, errors.New("email: IMAP settings are required")
	}
	if cfg.SMTPAddr == "" || cfg.SMTPUsername == "" || cfg.SMTPPassword == "" {
		return nil, errors.New("email: SMTP settings are required")
	}
	if cfg.From == "" {
		return nil, errors.New("email: From is required")
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.IMAPClientFactory == nil {
		cfg.IMAPClientFactory = defaultIMAPFactory
	}
	if cfg.SendMail == nil {
		cfg.SendMail = defaultSendMail
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "email" }

// Start polls INBOX at PollInterval, forwarding UNSEEN messages to
// the handler.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("email: handler is required")
	}
	c.logger.Info("email.started", slog.String("imap", c.cfg.IMAPAddr))
	c.inflight = new(transport.Inflight)
	defer c.inflight.Wait()

	tick := time.NewTicker(c.cfg.PollInterval)
	defer tick.Stop()
	for {
		if c.stopped.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := c.pollOnce(ctx, handler); err != nil {
				c.logger.Warn("email.poll_failed", slog.String("err", err.Error()))
			}
		}
	}
}

// Stop halts the polling loop.
func (c *Client) Stop() error {
	c.stopped.Store(true)
	return nil
}

// Deliver posts a plain-text message to the given email address via
// SMTP. body is prepended with ReplyHeader when configured.
func (c *Client) Deliver(_ context.Context, to, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	msg := buildMessage(c.cfg.From, to, body)
	return c.cfg.SendMail(c.cfg.SMTPAddr, c.cfg.From, []string{to}, msg,
		c.cfg.SMTPUsername, c.cfg.SMTPPassword)
}

// pollOnce opens an IMAP session, searches UNSEEN, dispatches each
// unread message, and closes the session.
func (c *Client) pollOnce(ctx context.Context, handler transport.Handler) error {
	client, err := c.cfg.IMAPClientFactory(c.cfg.IMAPAddr, c.cfg.IMAPUsername, c.cfg.IMAPPassword)
	if err != nil {
		return fmt.Errorf("email: connect: %w", err)
	}
	defer func() { _ = client.Close() }() //nolint:errcheck // best-effort close

	if _, err := client.Select(c.cfg.Mailbox, nil); err != nil {
		return fmt.Errorf("email: select: %w", err)
	}
	search, err := client.Search(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}, nil)
	if err != nil {
		return fmt.Errorf("email: search: %w", err)
	}
	nums := search.AllSeqNums()
	if len(nums) == 0 {
		return nil
	}
	set := imap.SeqSetNum(nums...)

	fetch := client.Fetch(set, &imap.FetchOptions{
		Envelope: true,
		// The full body first (extractBody reads the first non-header
		// section), then just the Authentication-Results header the
		// DKIM gate inspects.
		BodySection: []*imap.FetchItemBodySection{
			{},
			{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{authResultsHeader}, Peek: true},
		},
		BodyStructure: &imap.FetchItemBodyStructure{},
	})
	defer func() { _ = fetch.Close() }() //nolint:errcheck // best-effort close
	messages, err := fetch.Collect()
	if err != nil {
		return fmt.Errorf("email: fetch: %w", err)
	}
	for _, m := range messages {
		from, subject, body, ok := c.admit(m)
		if !ok {
			continue
		}
		msg := transport.IncomingMessage{
			From: from,
			Body: body,
			At:   time.Now().UTC(),
		}
		if m.Envelope != nil {
			msg.At = m.Envelope.Date
		}
		c.logger.Info("email.incoming", slog.String("from", from), slog.String("subject", subject))
		// The turn runs off the poll loop so one long turn does not hold
		// up other senders' mail.
		c.inflight.Do(func() {
			reply, hErr := handler.Handle(ctx, msg)
			if hErr != nil {
				c.logger.Error("email.handler_failed", slog.String("err", hErr.Error()))
				return
			}
			if reply != "" {
				if err := c.Deliver(ctx, from, reply); err != nil {
					c.logger.Error("email.send_failed", slog.String("err", err.Error()))
				}
			}
		})
	}
	// Mark the batch seen. With concurrent handling this happens as soon
	// as the turns are dispatched, so a long turn is not re-read (and
	// re-run) by the next poll; the trade-off is that a crash mid-turn
	// does not retry that mail.
	store := client.Store(set, &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}, nil)
	if store != nil {
		_ = store.Close() //nolint:errcheck // best-effort close after mark-seen
	}
	return nil
}

// -- helpers -----------------------------------------------------------

// admit decides whether a fetched message reaches the handler and
// returns its sender, subject and body. Empty senders or bodies are
// skipped; with RequireAuthResults, mail the MTA did not authenticate
// for the From domain is dropped with a warning.
func (c *Client) admit(m *imapclient.FetchMessageBuffer) (from, subject, body string, ok bool) {
	from = envelopeFrom(m)
	if m.Envelope != nil {
		subject = m.Envelope.Subject
	}
	body = extractBody(m)
	if body == "" {
		body = subject
	}
	if body == "" || from == "" {
		return "", "", "", false
	}
	if c.cfg.RequireAuthResults && !authResultsPass(m, from) {
		c.logger.Warn("email.dropped_unauthenticated",
			slog.String("from", from),
			slog.String("reason", "no Authentication-Results dkim=pass for the From domain"))
		return "", "", "", false
	}
	return from, subject, body, true
}

func envelopeFrom(m *imapclient.FetchMessageBuffer) string {
	if m == nil || m.Envelope == nil || len(m.Envelope.From) == 0 {
		return ""
	}
	a := m.Envelope.From[0]
	// Lower-cased so allowlist matching is case-insensitive.
	if a.Host == "" {
		return strings.ToLower(a.Mailbox)
	}
	return strings.ToLower(a.Mailbox + "@" + a.Host)
}

// extractBody pulls plain-text out of a fetched IMAP message. Full
// MIME multipart handling is left for a future upgrade; today we look
// at the first BodySection buffer and treat it as UTF-8 text.
func extractBody(m *imapclient.FetchMessageBuffer) string {
	if m == nil {
		return ""
	}
	for _, section := range m.BodySection {
		if len(section.Bytes) == 0 || isHeaderSection(section.Section) {
			continue
		}
		text := string(section.Bytes)
		text = stripHeaders(text)
		return strings.TrimSpace(text)
	}
	return ""
}

// stripHeaders drops everything up to the first blank line (RFC 5322
// header/body separator). Extremely primitive; real MIME parsing lives
// in a future PR.
func stripHeaders(raw string) string {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return raw[i+4:]
	}
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return raw[i+2:]
	}
	return raw
}

// buildMessage renders a plain-text RFC 5322 message.
func buildMessage(from, to, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: rousseau-agent reply\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// -- default factories -------------------------------------------------

// imapDialTLS is the TLS dial step of defaultIMAPFactory, held in a
// package-level var so tests can point the factory at a loopback IMAP
// server carrying a throwaway CA rather than a public host. Only
// tests reassign it.
var imapDialTLS = func(addr string) (*imapclient.Client, error) {
	return imapclient.DialTLS(addr, nil)
}

func defaultIMAPFactory(addr, user, pass string) (IMAPClient, error) {
	client, err := imapDialTLS(addr)
	if err != nil {
		return nil, err
	}
	if err := client.Login(user, pass).Wait(); err != nil {
		_ = client.Close() //nolint:errcheck // best-effort cleanup on login failure
		return nil, err
	}
	return imapAdapter{client}, nil
}

// imapAdapter wraps *imapclient.Client to satisfy IMAPClient.
type imapAdapter struct{ c *imapclient.Client }

func (a imapAdapter) Select(name string, opts *imap.SelectOptions) (*imap.SelectData, error) {
	return a.c.Select(name, opts).Wait()
}
func (a imapAdapter) Search(criteria *imap.SearchCriteria, opts *imap.SearchOptions) (*imap.SearchData, error) {
	return a.c.Search(criteria, opts).Wait()
}
func (a imapAdapter) Fetch(seqSet imap.NumSet, options *imap.FetchOptions) FetchCommand {
	return fetchAdapter{a.c.Fetch(seqSet, options)}
}
func (a imapAdapter) Store(seqSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) StoreCommand {
	return storeAdapter{a.c.Store(seqSet, flags, options)}
}
func (a imapAdapter) Close() error { return a.c.Close() }

type fetchAdapter struct{ *imapclient.FetchCommand }

func (f fetchAdapter) Collect() ([]*imapclient.FetchMessageBuffer, error) {
	return f.FetchCommand.Collect()
}
func (f fetchAdapter) Close() error { return f.FetchCommand.Close() }

type storeAdapter struct{ *imapclient.FetchCommand }

func (s storeAdapter) Close() error {
	if s.FetchCommand == nil {
		return nil
	}
	return s.FetchCommand.Close()
}

func defaultSendMail(addr, from string, to []string, msg []byte, user, pass string) error {
	host, _, ok := splitHostPort(addr)
	if !ok {
		return fmt.Errorf("email: bad SMTP addr %q", addr)
	}
	auth := smtp.PlainAuth("", user, pass, host)
	return smtp.SendMail(addr, auth, from, to, msg)
}

func splitHostPort(addr string) (host, port string, ok bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", false
	}
	return addr[:i], addr[i+1:], true
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
