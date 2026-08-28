//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Sender is the narrow subset of whatsmeow.Client that the outbound
// send path uses. Extracted so unit tests can inject a fake and verify
// what the transport would have sent without touching the network.
type Sender interface {
	// SendText delivers a plain-text message to chat.
	SendText(ctx context.Context, chat types.JID, body string) error
	// SendPresence emits a chat-presence update ("typing…"/"paused")
	// scoped to the text media type.
	SendPresence(ctx context.Context, chat types.JID, state types.ChatPresence) error
}

// MessageEditor is the OPTIONAL in-place-edit capability. It is a
// separate interface rather than extra methods on Sender so that
// existing Sender implementations (and every test fake) keep
// compiling: a sink type-asserts for it and silently posts new
// messages when it is absent.
//
// WhatsApp allows editing a message for roughly 15 minutes after it
// was sent; past that the edit fails and the progress reporter falls
// back to a new message.
type MessageEditor interface {
	// SendTextWithID delivers body and returns the server message ID,
	// which EditText needs later.
	SendTextWithID(ctx context.Context, chat types.JID, body string) (string, error)
	// EditText replaces the body of a previously-sent message.
	EditText(ctx context.Context, chat types.JID, id, body string) error
}

// Reactor is the OPTIONAL emoji-reaction capability. Same shape as
// MessageEditor: kept off Sender so unit fakes stay small, and callers
// type-assert before use. Reactions confirm receipt/completion in-band
// without adding lines to the thread.
type Reactor interface {
	// React sends emoji as a reaction to targetID in chat. senderJID is
	// the JID that sent the target message (whatsmeow needs it to build
	// the reaction key). An empty emoji removes any prior reaction.
	React(ctx context.Context, chat, senderJID types.JID, targetID, emoji string) error
}

// wmSender adapts a *whatsmeow.Client to the Sender interface.
type wmSender struct{ wm *whatsmeow.Client }

func newWMSender(wm *whatsmeow.Client) *wmSender { return &wmSender{wm: wm} }

// SendText satisfies Sender.
func (s *wmSender) SendText(ctx context.Context, chat types.JID, body string) error {
	_, err := s.wm.SendMessage(ctx, chat, &waProto.Message{
		Conversation: proto.String(body),
	})
	return err
}

// SendPresence satisfies Sender.
func (s *wmSender) SendPresence(ctx context.Context, chat types.JID, state types.ChatPresence) error {
	return s.wm.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText)
}

// SendTextWithID satisfies MessageEditor.
func (s *wmSender) SendTextWithID(ctx context.Context, chat types.JID, body string) (string, error) {
	resp, err := s.wm.SendMessage(ctx, chat, &waProto.Message{
		Conversation: proto.String(body),
	})
	return resp.ID, err
}

// EditText satisfies MessageEditor.
func (s *wmSender) EditText(ctx context.Context, chat types.JID, id, body string) error {
	edit := s.wm.BuildEdit(chat, id, &waProto.Message{
		Conversation: proto.String(body),
	})
	_, err := s.wm.SendMessage(ctx, chat, edit)
	return err
}

// React satisfies Reactor. targetID is the ID of the message being
// reacted to; senderJID is who sent it (self for outbound, peer for
// inbound). Whatsmeow's BuildReaction packs both into the reaction
// key so recipients can dedupe.
func (s *wmSender) React(ctx context.Context, chat, senderJID types.JID, targetID, emoji string) error {
	msg := s.wm.BuildReaction(chat, senderJID, targetID, emoji)
	_, err := s.wm.SendMessage(ctx, chat, msg)
	return err
}

// Compile-time checks: the real client supports both optional
// capabilities.
var (
	_ MessageEditor = (*wmSender)(nil)
	_ Reactor       = (*wmSender)(nil)
)

// parseJID parses a JID string like "15551234567@s.whatsapp.net" into
// the whatsmeow types. It rejects empty inputs and surfaces the
// parser's error verbatim.
func parseJID(s string) (types.JID, error) {
	if s == "" {
		return types.JID{}, fmt.Errorf("whatsapp: empty JID")
	}
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.JID{}, fmt.Errorf("whatsapp: parse JID %q: %w", s, err)
	}
	return jid, nil
}

// wmDownloader adapts a *whatsmeow.Client to the Downloader interface.
type wmDownloader struct{ wm *whatsmeow.Client }

func newWMDownloader(wm *whatsmeow.Client) *wmDownloader { return &wmDownloader{wm: wm} }

// Download satisfies Downloader.
func (d *wmDownloader) Download(ctx context.Context, msg DownloadableAudio) ([]byte, string, error) {
	// The whatsmeow Download method accepts anything satisfying
	// its whatsmeow.DownloadableMessage interface. *waProto.AudioMessage
	// satisfies both DownloadableAudio (ours) and whatsmeow's.
	audio, ok := msg.(whatsmeow.DownloadableMessage)
	if !ok {
		return nil, "", fmt.Errorf("whatsapp: message is not whatsmeow.DownloadableMessage")
	}
	b, err := d.wm.Download(ctx, audio)
	if err != nil {
		return nil, msg.GetMimetype(), err
	}
	return b, msg.GetMimetype(), nil
}
