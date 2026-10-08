//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// WhatsApp groups are skipped before routing, so every message that
// reaches the handler is one-to-one.
func TestResolveInbound_DirectChatIsDirect(t *testing.T) {
	sender := jid("15550001111", 0)
	res := ResolveInbound(msgEvent(sender, sender, false, false, "hi"), nil)
	require.Equal(t, SkipNone, res.Skip)
	assert.True(t, res.Msg.IsDirect)
}

func TestResolveInbound_GroupNeverReachesHandler(t *testing.T) {
	sender := jid("15550001111", 0)
	group := types.JID{User: "1203630", Server: "g.us"}
	res := ResolveInbound(msgEvent(sender, group, false, true, "hi"), nil)
	assert.Equal(t, SkipGroup, res.Skip)
	assert.False(t, res.Msg.IsDirect)
}

func TestDispatch_ImageMessageIsDirect(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	h := &captureHandler{}
	Dispatch(context.Background(), DispatchInput{
		Event:      imageEvent(sender, sender.ToNonAD(), "image/png", ""),
		OwnID:      &own,
		Sender:     &fakeSender{},
		Downloader: &fakeDownloader{audio: pngHeader, mimetype: "image/png"},
		Handler:    h,
		Logger:     silentLogger(),
	})
	require.Len(t, h.got.Attachments, 1)
	assert.True(t, h.got.IsDirect)
}

func TestDispatch_VoiceNoteIsDirect(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
			Timestamp:     time.Unix(0, 0),
		},
		Message: &waProto.Message{AudioMessage: &waProto.AudioMessage{Mimetype: proto.String("audio/ogg")}},
	}
	h := &captureHandler{}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       &own,
		Sender:      &fakeSender{},
		Downloader:  &fakeDownloader{audio: []byte{1, 2, 3}, mimetype: "audio/ogg"},
		Transcriber: &fakeTranscriber{text: "hello"},
		Handler:     h,
		Logger:      silentLogger(),
	})
	require.Equal(t, "hello", h.got.Body)
	assert.True(t, h.got.IsDirect)
}
