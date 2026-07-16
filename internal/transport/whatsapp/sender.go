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
