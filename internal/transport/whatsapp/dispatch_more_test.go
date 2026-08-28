//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// handlerFunc adapts a zero-argument closure to transport.Handler.
func handlerFunc(fn func() (string, error)) transport.HandlerFunc {
	return func(context.Context, transport.IncomingMessage) (string, error) { return fn() }
}

// handlerCapturing exposes the resolved From to the assertion.
func handlerCapturing(fn func(from string) (string, error)) transport.HandlerFunc {
	return func(_ context.Context, msg transport.IncomingMessage) (string, error) { return fn(msg.From) }
}

// audioEvent builds a voice-note event with no text body, which is
// what ResolveInbound classifies as SkipEmptyText and Dispatch then
// tries to recover by transcribing.
func audioEvent(sender, chat types.JID, fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: chat, IsFromMe: fromMe},
			Timestamp:     time.Unix(1700000000, 0),
		},
		Message: &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				Mimetype: proto.String("audio/ogg; codecs=opus"),
				Seconds:  proto.Uint32(4),
			},
		},
	}
}

// TestDispatch_NilLoggerDoesNotPanic covers the slog.Default()
// fallback: callers may leave Logger unset.
func TestDispatch_NilLoggerDoesNotPanic(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	send := &fakeSender{}
	assert.NotPanics(t, func() {
		Dispatch(context.Background(), DispatchInput{
			Event:   msgEvent(sender, sender.ToNonAD(), false, false, "hi"),
			OwnID:   &own,
			Sender:  send,
			Handler: handlerReturning("ok", nil),
			Header:  " ",
		})
	})
	require.Len(t, send.sent, 1)
}

// TestDispatch_OwnOutboundToThirdPartyIsNotAnswered is the
// safety-critical guard: the account holder messaging someone else
// from their phone must never draw a reply into that chat.
func TestDispatch_OwnOutboundToThirdPartyIsNotAnswered(t *testing.T) {
	own := jid("15551234567", 21)
	me := jid("15551234567", 3) // another linked device of ours
	third := jid("447700900000", 0)

	logs := &logBuffer{}
	send := &fakeSender{}
	handlerCalled := false
	Dispatch(context.Background(), DispatchInput{
		Event:  msgEvent(me, third.ToNonAD(), true, false, "talking to a friend"),
		OwnID:  &own,
		Sender: send,
		Handler: handlerFunc(func() (string, error) {
			handlerCalled = true
			return "should not happen", nil
		}),
		Logger: logs.newLogger(),
	})
	assert.False(t, handlerCalled)
	assert.Empty(t, send.sent)
	assert.Empty(t, send.presence)
	assert.True(t, logs.has("whatsapp.skipped_own_outbound"))
}

// TestDispatch_SelfChatVoiceNoteIsAttributedToOwnJID exercises
// resolveFrom's IsFromMe branch: a voice note the operator sends
// themselves must be attributed to the account JID, not the LID
// WhatsApp reports as the sender.
func TestDispatch_SelfChatVoiceNoteIsAttributedToOwnJID(t *testing.T) {
	own := jid("15551234567", 21)
	lid := types.JID{User: "998877665544", Server: types.HiddenUserServer, Device: 3}

	var seenFrom string
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       audioEvent(lid, own.ToNonAD(), true),
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{audio: []byte{0x01}, mimetype: "audio/ogg"},
		Transcriber: &fakeTranscriber{text: "  remind me later  "},
		Handler: handlerCapturing(func(from string) (string, error) {
			seenFrom = from
			return "noted", nil
		}),
		Header: " ",
		Logger: silentLogger(),
	})
	assert.Equal(t, own.ToNonAD().String(), seenFrom)
	require.Len(t, send.sent, 1)
	assert.Equal(t, "noted", send.sent[0])
}

func TestDispatch_TranscriberErrorSuppressesReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("447700900000", 0)
	logs := &logBuffer{}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       audioEvent(sender, sender.ToNonAD(), false),
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{audio: []byte{0x01}, mimetype: "audio/ogg"},
		Transcriber: &fakeTranscriber{err: errors.New("whisper exploded")},
		Handler:     handlerReturning("unreachable", nil),
		Logger:      logs.newLogger(),
	})
	assert.Empty(t, send.sent)
	assert.True(t, logs.has("whatsapp.transcribe_failed"))
}

// TestDispatch_DownloaderWithoutTranscriberIgnoresAudio covers the
// "half configured" case — a Downloader but no Transcriber.
func TestDispatch_DownloaderWithoutTranscriberIgnoresAudio(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("447700900000", 0)
	logs := &logBuffer{}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:      audioEvent(sender, sender.ToNonAD(), false),
		OwnID:      &own,
		Sender:     send,
		Downloader: &fakeDownloader{audio: []byte{0x01}},
		Handler:    handlerReturning("unreachable", nil),
		Logger:     logs.newLogger(),
	})
	assert.Empty(t, send.sent)
	assert.True(t, logs.has("transcriber_not_configured"))
}

func TestTranscribeAudio_NilMessageRejected(t *testing.T) {
	_, err := transcribeAudio(context.Background(), &fakeDownloader{}, &fakeTranscriber{}, nil, silentLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil audio message")
}

func TestSetPresence_NilSenderIsNoop(t *testing.T) {
	assert.NotPanics(t, func() {
		setPresence(context.Background(), nil, types.JID{}, types.ChatPresenceComposing, silentLogger())
	})
}

// TestSetPresence_ErrorIsLoggedNotFatal — a failed typing indicator
// must never abort the reply flow.
func TestSetPresence_ErrorIsLoggedNotFatal(t *testing.T) {
	logs := &logBuffer{}
	send := &fakeSender{presErr: errors.New("presence rejected")}
	setPresence(context.Background(), send, types.JID{User: "1", Server: types.DefaultUserServer},
		types.ChatPresenceComposing, logs.newLogger())
	assert.True(t, logs.has("whatsapp.presence_failed"))
}

// TestDispatch_PresenceFailureStillDeliversReply is the behavioural
// counterpart: presence errors are cosmetic, the reply still lands.
func TestDispatch_PresenceFailureStillDeliversReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("447700900000", 0)
	send := &fakeSender{presErr: errors.New("nope")}
	Dispatch(context.Background(), DispatchInput{
		Event:   msgEvent(sender, sender.ToNonAD(), false, false, "hello"),
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("hi back", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})
	require.Len(t, send.sent, 1)
	assert.Equal(t, "hi back", send.sent[0])
}

func TestExtractText_NilMessage(t *testing.T) {
	assert.Empty(t, extractText(nil))
}
