//go:build !no_whatsmeow

package whatsapp

import (
	"strings"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// SkipReason categorises why an inbound event was not forwarded to the
// handler. Emitted from ResolveInbound; useful for logging and tests.
type SkipReason string

const (
	// SkipNone indicates the event should be processed.
	SkipNone SkipReason = ""
	// SkipGroup indicates the event was a group message.
	SkipGroup SkipReason = "group"
	// SkipOwnDevice indicates the event was our own outbound message
	// echoing back — a reply this linked device just sent. Loop
	// prevention.
	SkipOwnDevice SkipReason = "own_device"
	// SkipOwnOutbound indicates the event was the account holder
	// messaging a third party from another linked device (e.g. their
	// primary phone). Must not be treated as inbound to the agent —
	// otherwise the reply would land in the third-party's chat.
	SkipOwnOutbound SkipReason = "own_outbound"
	// SkipEmptyText indicates the message carried no text content.
	SkipEmptyText SkipReason = "empty_text"
)

// Resolved is the outcome of resolving a whatsmeow message event into
// the transport-agnostic shape the router expects.
type Resolved struct {
	// Msg is the normalised message. Zero value if Skip is set.
	Msg transport.IncomingMessage
	// Chat is the reply destination for send calls; the "chat" field of
	// the WhatsApp event, not the sender's device-scoped JID.
	Chat types.JID
	// Skip is set when the event should not be handled. Msg and Chat
	// are undefined in that case.
	Skip SkipReason
}

// ResolveInbound extracts a normalised IncomingMessage from a raw
// whatsmeow message event, applying every routing rule the transport
// enforces: group filter, own-device loop prevention, LID → account
// JID substitution for self-chat, multi-device suffix stripping.
//
// It is pure — no I/O, no logging, no globals — so it can be tested
// exhaustively without a live whatsmeow client.
func ResolveInbound(evt *events.Message, ownID *types.JID) Resolved {
	if evt == nil || evt.Message == nil {
		return Resolved{Skip: SkipEmptyText}
	}
	if evt.Info.IsGroup {
		return Resolved{Skip: SkipGroup}
	}
	// A message with an unparseable / empty sender JID cannot be routed
	// safely; drop it rather than pass a malformed From to the handler.
	// Discovered by FuzzResolveInbound.
	if evt.Info.Sender.User == "" || evt.Info.Sender.Server == "" {
		return Resolved{Skip: SkipEmptyText}
	}
	// Loop-prevention: skip messages sent by *this* linked device
	// (our own replies echoing back). Messages from the account's
	// other linked devices (the primary phone testing "message
	// yourself") carry IsFromMe=true but a different device ID.
	if evt.Info.IsFromMe && ownID != nil && evt.Info.Sender.Device == ownID.Device {
		return Resolved{Skip: SkipOwnDevice}
	}
	body := strings.TrimSpace(extractText(evt.Message))
	if body == "" {
		return Resolved{Skip: SkipEmptyText}
	}

	// Sender normalisation.
	//  1. Strip the multi-device address suffix so allowlists written
	//     as the plain user JID match regardless of which linked
	//     device sent the message.
	//  2. When the account holder is the sender (IsFromMe) *and the
	//     conversation is the self-chat*, WhatsApp reports the sender
	//     as the account's LID (a privacy hash), not the phone JID.
	//     Substitute our own account JID so operators can allowlist
	//     "<phone>@s.whatsapp.net" and have self-chat testing route
	//     correctly. If IsFromMe is set but Chat is *not* self-chat,
	//     this is the account holder messaging a third party from
	//     another linked device — the message must not pass the
	//     allowlist and the agent must not reply into that chat.
	from := evt.Info.Sender.ToNonAD()
	if evt.Info.IsFromMe && ownID != nil {
		ownJID := ownID.ToNonAD()
		chatJID := evt.Info.Chat.ToNonAD()
		senderJID := evt.Info.Sender.ToNonAD()
		// Self-chat detection covers two representations WhatsApp uses:
		//   1. Chat equals our stored account JID (typically the PN
		//      form, e.g. "447…@s.whatsapp.net").
		//   2. Chat equals Sender — happens when both are reported as
		//      our own LID (newer WhatsApp behaviour).
		// If neither holds, this is an outbound message to a third
		// party echoed via another linked device; the agent must not
		// answer it.
		isSelfChat := chatJID == ownJID || chatJID == senderJID
		if !isSelfChat {
			return Resolved{Skip: SkipOwnOutbound}
		}
		from = ownJID
	}

	return Resolved{
		Msg: transport.IncomingMessage{
			From: from.String(),
			Body: body,
			At:   evt.Info.Timestamp,
		},
		Chat: evt.Info.Chat,
	}
}

// PrependHeader returns text with the header attached. Empty header
// falls back to DefaultReplyHeader; a single space " " (or any
// header without non-whitespace) disables the prefix entirely.
func PrependHeader(text, header string) string {
	if strings.TrimSpace(header) == "" && header != "" {
		return text
	}
	if header == "" {
		header = DefaultReplyHeader
	}
	return header + text
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
