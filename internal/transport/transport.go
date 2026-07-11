// Package transport abstracts inbound / outbound messaging channels
// (WhatsApp today; Telegram, Slack, etc. later).
//
// A Transport receives IncomingMessages and hands them to a Handler.
// The Handler returns the text to reply with. The Transport is
// responsible for delivering that reply back to the sender.
package transport

import (
	"context"
	"time"
)

// IncomingMessage is a normalised inbound message.
type IncomingMessage struct {
	// From is a platform-specific stable sender identifier
	// (WhatsApp JID, Telegram chat ID, …).
	From string
	// Body is the raw text content. Media is not currently supported.
	Body string
	// At is the server-reported timestamp.
	At time.Time
}

// Handler processes an incoming message and returns the reply text.
// Returning an empty reply skips sending anything.
type Handler interface {
	Handle(ctx context.Context, msg IncomingMessage) (string, error)
}

// HandlerFunc adapts an ordinary function to Handler.
type HandlerFunc func(ctx context.Context, msg IncomingMessage) (string, error)

// Handle satisfies Handler.
func (f HandlerFunc) Handle(ctx context.Context, msg IncomingMessage) (string, error) {
	return f(ctx, msg)
}

// Transport is a bidirectional messaging channel. Start is expected to
// block until ctx is cancelled or Stop is called.
type Transport interface {
	// Name is a stable identifier ("whatsapp", "telegram", …).
	Name() string
	// Start attaches the handler and pumps messages until ctx is
	// cancelled or Stop is called.
	Start(ctx context.Context, handler Handler) error
	// Stop terminates the transport. Safe to call multiple times.
	Stop() error
}
