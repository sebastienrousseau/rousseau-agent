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

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// pngHeader is the exact 8-byte PNG signature. `http.DetectContentType`
// treats these first bytes as authoritative — the shortest legal
// prefix that makes the sniffer say "image/png". Enough to exercise
// the media.Policy accept path without pulling a fixture image into
// the tree.
var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// captureHandler records the IncomingMessage the router would see and
// returns a fixed reply. Lets a test assert that the transport built
// exactly the shape the model layer expects.
type captureHandler struct {
	got   transport.IncomingMessage
	reply string
}

func (c *captureHandler) Handle(_ context.Context, msg transport.IncomingMessage) (string, error) {
	c.got = msg
	return c.reply, nil
}

func imageEvent(sender, chat types.JID, envelopeMIME, caption string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: chat},
			Timestamp:     time.Unix(1_700_000_000, 0),
		},
		Message: &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				Mimetype: proto.String(envelopeMIME),
				Caption:  proto.String(caption),
				Width:    proto.Uint32(64),
				Height:   proto.Uint32(64),
			},
		},
	}
}

func TestDispatch_ImageDownloadedAndDeliveredWithCaption(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "look at this")

	h := &captureHandler{reply: "seen"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:      evt,
		OwnID:      &own,
		Sender:     send,
		Downloader: &fakeDownloader{audio: pngHeader, mimetype: "image/png"},
		Handler:    h,
		Header:     " ",
		Logger:     silentLogger(),
	})

	require.Len(t, send.sent, 1)
	assert.Equal(t, "seen", send.sent[0])
	assert.Equal(t, "look at this", h.got.Body, "caption must flow through as Body")
	require.Len(t, h.got.Attachments, 1)
	assert.Equal(t, "image/png", h.got.Attachments[0].MediaType)
	assert.Equal(t, pngHeader, h.got.Attachments[0].Data)
}

func TestDispatch_ImageWithoutCaptionStillRoutes(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "")

	h := &captureHandler{reply: "seen"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:      evt,
		OwnID:      &own,
		Sender:     send,
		Downloader: &fakeDownloader{audio: pngHeader, mimetype: "image/png"},
		Handler:    h,
		Header:     " ",
		Logger:     silentLogger(),
	})

	require.Len(t, send.sent, 1)
	assert.Empty(t, h.got.Body, "no caption → empty body")
	require.Len(t, h.got.Attachments, 1)
}

func TestDispatch_ImageNoDownloaderIsIgnored(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "")

	h := &captureHandler{}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: h,
		Logger:  silentLogger(),
		// no Downloader
	})
	assert.Empty(t, send.sent)
	assert.Empty(t, h.got.From, "handler must not run when the image cannot be fetched")
}

func TestDispatch_ImageDownloadFailureIsLoggedNotDelivered(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "")

	h := &captureHandler{reply: "should not fire"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:      evt,
		OwnID:      &own,
		Sender:     send,
		Downloader: &fakeDownloader{err: errors.New("wa: net")},
		Handler:    h,
		Logger:     silentLogger(),
	})
	assert.Empty(t, send.sent)
	assert.Empty(t, h.got.From)
}

func TestDispatch_ImageOversizeIsDropped(t *testing.T) {
	// A 3-byte per-image cap forces every real payload to fail the
	// policy check — the drop is quiet, no handler call, no reply.
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "")

	h := &captureHandler{reply: "unreached"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{audio: pngHeader, mimetype: "image/png"},
		Handler:     h,
		MediaPolicy: media.Policy{MaxImageBytes: 3},
		Logger:      silentLogger(),
	})
	assert.Empty(t, send.sent)
	assert.Empty(t, h.got.From, "oversize image must not reach the handler")
}

func TestDispatch_ImageDisallowedMIMEIsDropped(t *testing.T) {
	// The bytes sniff as PNG. Constrain the allowlist to JPEG so the
	// policy rejects — proves the sniffed MIME (not the envelope's
	// self-report) is what the check runs against.
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/png", "")

	h := &captureHandler{reply: "unreached"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:       evt,
		OwnID:       &own,
		Sender:      send,
		Downloader:  &fakeDownloader{audio: pngHeader, mimetype: "image/png"},
		Handler:     h,
		MediaPolicy: media.Policy{AllowedMIMEs: []string{"image/jpeg"}},
		Logger:      silentLogger(),
	})
	assert.Empty(t, send.sent)
	assert.Empty(t, h.got.From, "disallowed MIME must not reach the handler")
}

func TestDispatch_ImageEnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	// Envelope claims JPEG, bytes are PNG. Policy allowlist admits
	// both — the pipeline delivers the sniffed MIME (image/png) as
	// the attachment's canonical value, and logs the disagreement.
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := imageEvent(sender, sender.ToNonAD(), "image/jpeg", "")

	h := &captureHandler{reply: "ack"}
	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:      evt,
		OwnID:      &own,
		Sender:     send,
		Downloader: &fakeDownloader{audio: pngHeader, mimetype: "image/jpeg"},
		Handler:    h,
		Header:     " ",
		Logger:     silentLogger(),
	})
	require.Len(t, h.got.Attachments, 1)
	assert.Equal(t, "image/png", h.got.Attachments[0].MediaType,
		"sniffed MIME wins over the envelope's claim")
}

func TestDownloadImage_NilMessageRejected(t *testing.T) {
	att, err := downloadImage(context.Background(), &fakeDownloader{}, nil, media.Policy{}, silentLogger())
	require.Error(t, err)
	assert.Nil(t, att)
}
