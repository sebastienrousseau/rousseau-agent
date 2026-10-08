// Package transport abstracts inbound / outbound messaging channels
// (WhatsApp today; Telegram, Slack, etc. later).
//
// A Transport receives IncomingMessages and hands them to a Handler.
// The Handler returns the text to reply with. The Transport is
// responsible for delivering that reply back to the sender.
package transport

import (
	"context"
	"log/slog"
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
	// Conversation is where the reply goes: the chat, channel, room
	// or group the message arrived in. It equals From in a direct
	// chat and differs in a group, where From is one member. Empty
	// means the transport did not distinguish the two.
	Conversation string
	// MessageID is the platform's identifier for this inbound message
	// (Slack ts, Discord snowflake, Matrix event id, Mail Message-ID,
	// …), for quoting, reacting or deduplicating. Empty when the
	// platform has none.
	MessageID string
	// IsDirect is true when the message arrived in a one-to-one
	// conversation with the bot (a DM, a private chat, an email), and
	// false in a group, channel or room where other members read the
	// reply. Each transport sets it; false is the safe default for a
	// transport that cannot tell.
	IsDirect bool
	// Thread identifies the thread or topic the message belongs to
	// (Slack thread_ts, Telegram forum topic, mail subject) so the
	// transport can keep its reply in the same thread. Empty when the
	// message is not in one.
	Thread string
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

// IsGroup reports whether msg arrived in a conversation other members
// read: the transport did not mark it direct and it names a
// conversation other than the sender. A message with no Conversation,
// or one equal to From, is one-to-one by construction.
func IsGroup(msg IncomingMessage) bool {
	return !msg.IsDirect && msg.Conversation != "" && msg.Conversation != msg.From
}

// ConversationKey is the key a sender's turn and session live under:
// the sender in a direct conversation, and "<sender>#<conversation>"
// in a group, so a sender's private history never reaches a group and
// a group's history stays in that group.
func ConversationKey(msg IncomingMessage) string {
	if IsGroup(msg) {
		return msg.From + "#" + msg.Conversation
	}
	return msg.From
}

// DropGroups ignores every group message (see [IsGroup]) before it
// reaches next, so nothing downstream — control verbs, rate-limit
// notices, the agent — answers in a group the operator did not
// enable.
func DropGroups(next Handler, logger *slog.Logger) Handler {
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		if IsGroup(msg) {
			logger.Debug("transport.group_ignored", slog.String("from", msg.From))
			return "", nil
		}
		return next.Handle(ctx, msg)
	})
}
