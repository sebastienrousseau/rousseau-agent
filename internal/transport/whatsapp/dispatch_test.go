package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
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

type fakeSender struct {
	mu        sync.Mutex
	sent      []string
	presence  []types.ChatPresence
	sendErr   error
	presErr   error
}

func (f *fakeSender) SendText(_ context.Context, _ types.JID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, body)
	return nil
}

func (f *fakeSender) SendPresence(_ context.Context, _ types.JID, state types.ChatPresence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presence = append(f.presence, state)
	return f.presErr
}

type fakeDownloader struct {
	audio    []byte
	mimetype string
	err      error
}

func (f *fakeDownloader) Download(_ context.Context, _ DownloadableAudio) ([]byte, string, error) {
	return f.audio, f.mimetype, f.err
}

type fakeTranscriber struct {
	text string
	err  error
	seen [][]byte
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, _ string) (string, error) {
	f.seen = append(f.seen, audio)
	return f.text, f.err
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func handlerReturning(reply string, err error) transport.HandlerFunc {
	return func(_ context.Context, _ transport.IncomingMessage) (string, error) {
		return reply, err
	}
}

func TestDispatch_TextRoundtrip(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("hello back", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})
	require.Len(t, send.sent, 1)
	assert.Equal(t, "hello back", send.sent[0])
	assert.Equal(t, []types.ChatPresence{types.ChatPresenceComposing, types.ChatPresencePaused}, send.presence)
}

func TestDispatch_DefaultHeaderAppliedWhenBlank(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("body", nil),
		Header:  "",
		Logger:  silentLogger(),
	})
	require.Len(t, send.sent, 1)
	assert.Equal(t, DefaultReplyHeader+"body", send.sent[0])
}

func TestDispatch_HandlerErrorSkipsSend(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", errors.New("boom")),
		Logger:  silentLogger(),
	})
	assert.Empty(t, send.sent)
	// Presence indicator still fires (composing + paused).
	assert.Len(t, send.presence, 2)
}

func TestDispatch_EmptyReplyDoesNotSend(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", nil),
		Logger:  silentLogger(),
	})
	assert.Empty(t, send.sent)
}

func TestDispatch_SkippedEventsDoNotSend(t *testing.T) {
	own := jid("447906009073", 21)
	// Own-device echo — must not roundtrip.
	evt := msgEvent(own, own.ToNonAD(), true, false, "our own reply")
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("should not be called", nil),
		Logger:  silentLogger(),
	})
	assert.Empty(t, send.sent)
	assert.Empty(t, send.presence)
}

func TestDispatch_AudioTranscribedAndDelivered(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: sender, Chat: sender.ToNonAD(), IsFromMe: false,
			},
			Timestamp: time.Unix(0, 0),
		},
		Message: &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				Mimetype: proto.String("audio/ogg; codecs=opus"),
				Seconds:  proto.Uint32(3),
			},
		},
	}

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{audio: []byte{0x01, 0x02, 0x03}, mimetype: "audio/ogg"},
		Transcriber: &fakeTranscriber{text: "hello world"},
		Handler:     handlerReturning("nice", nil),
		Header:      " ",
		Logger:      silentLogger(),
	})
	require.Len(t, send.sent, 1)
	assert.Equal(t, "nice", send.sent[0])
}

func TestDispatch_AudioNoTranscriberIsIgnored(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
		},
		Message: &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				Mimetype: proto.String("audio/ogg"),
				Seconds:  proto.Uint32(2),
			},
		},
	}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("nope", nil),
		Logger:  silentLogger(),
	})
	assert.Empty(t, send.sent)
}

func TestDispatch_AudioDownloadFailureLogged(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
		},
		Message: &waProto.Message{AudioMessage: &waProto.AudioMessage{Mimetype: proto.String("audio/ogg")}},
	}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{err: errors.New("net")},
		Transcriber: &fakeTranscriber{text: "unused"},
		Handler:     handlerReturning("nope", nil),
		Logger:      silentLogger(),
	})
	assert.Empty(t, send.sent)
}

func TestDispatch_SendFailureIsLoggedNotPanicked(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{sendErr: errors.New("network")}
	assert.NotPanics(t, func() {
		Dispatch(context.Background(), DispatchInput{
			Event:   evt,
			OwnID:   &own,
			Sender:  send,
			Handler: handlerReturning("body", nil),
			Header:  " ",
			Logger:  silentLogger(),
		})
	})
}
