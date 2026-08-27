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
	// Body is the raw text content. May coexist with Attachments —
	// e.g. an image message with a caption sets both.
	Body string
	// At is the server-reported timestamp.
	At time.Time
	// Attachments carries binary payloads the transport downloaded and
	// size/mime-verified before delivery. Empty for pure-text
	// messages. The transport is responsible for sniffing the MIME
	// (never trust the envelope) and enforcing the operator's media
	// policy before populating this slice.
	Attachments []Attachment
}

// Attachment is a downloaded, size- and MIME-verified binary payload
// attached to an IncomingMessage. Transports build these; the router
// converts each into an [agent.ContentImage] block when routing to
// the model. Keeping the shape neutral (no direct agent import) means
// audio and other future media can reuse the field without a further
// change here.
type Attachment struct {
	// MediaType is the sniffed MIME type ("image/png",
	// "image/jpeg", …). Never the envelope-reported value.
	MediaType string
	// Data is the raw bytes.
	Data []byte
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
