//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Downloader downloads media attached to a whatsmeow message. Extracted
// so tests can inject a fixture-returning fake and exercise voice-note
// handling without a live client.
type Downloader interface {
	// Download fetches the media payload for a downloadable message.
	// mimetype is returned so the transcriber knows the audio codec.
	Download(ctx context.Context, msg DownloadableAudio) (bytes []byte, mimetype string, err error)
}

// DownloadableAudio is the subset of *waProto.AudioMessage that the
// downloader needs. Isolated so tests do not need to construct a full
// whatsmeow message.
type DownloadableAudio interface {
	GetMimetype() string
	GetSeconds() uint32
}

// Transcriber lives in types.go so the no_whatsmeow build can still
// export the interface without pulling whatsmeow imports.

// DispatchInput bundles every dependency Dispatch touches. Explicit
// parameters, no globals — this is the seam that makes the transport
// unit-testable without a whatsmeow session.
type DispatchInput struct {
	Event       *events.Message
	OwnID       *types.JID
	Sender      Sender
	Downloader  Downloader
	Handler     transport.Handler
	Transcriber Transcriber
	Header      string
	Logger      *slog.Logger
}

// Dispatch processes a single inbound whatsmeow message: resolves it
// through ResolveInbound, transcribes voice notes if a Transcriber is
// configured, invokes the transport.Handler, and delivers the reply
// via Sender with a typing indicator on both edges. All I/O errors are
// logged; nothing panics.
func Dispatch(ctx context.Context, in DispatchInput) {
	log := in.Logger
	if log == nil {
		log = slog.Default()
	}

	res := ResolveInbound(in.Event, in.OwnID)
	if res.Skip == SkipNone {
		handleTextMessage(ctx, in, res, log)
		return
	}

	// Voice notes are the only skip-reason we deliberately try to
	// recover: an audio-only message with no text still deserves a
	// reply if we can transcribe it.
	if audioMsg := in.Event.Message.GetAudioMessage(); audioMsg != nil && res.Skip == SkipEmptyText {
		if in.Transcriber == nil || in.Downloader == nil {
			log.Info("whatsapp.audio_ignored",
				slog.String("reason", "transcriber_not_configured"),
				slog.Uint64("seconds", uint64(audioMsg.GetSeconds())))
			return
		}
		text, err := transcribeAudio(ctx, in.Downloader, in.Transcriber, audioMsg, log)
		if err != nil {
			log.Error("whatsapp.transcribe_failed", slog.String("err", err.Error()))
			return
		}
		res = Resolved{
			Msg: transport.IncomingMessage{
				From: resolveFrom(in.Event, in.OwnID).String(),
				Body: text,
				At:   in.Event.Info.Timestamp,
			},
			Chat: in.Event.Info.Chat,
		}
		handleTextMessage(ctx, in, res, log)
		return
	}

	if res.Skip != SkipEmptyText {
		log.Debug("whatsapp.skipped", slog.String("reason", string(res.Skip)))
	}
}

// resolveFrom mirrors the sender-normalisation in ResolveInbound. It
// is used by the audio-transcription branch, which constructs its own
// Resolved after downloading media.
func resolveFrom(evt *events.Message, ownID *types.JID) types.JID {
	from := evt.Info.Sender.ToNonAD()
	if evt.Info.IsFromMe && ownID != nil {
		from = ownID.ToNonAD()
	}
	return from
}

func handleTextMessage(ctx context.Context, in DispatchInput, res Resolved, log *slog.Logger) {
	log.Info("whatsapp.incoming", slog.String("from", res.Msg.From))

	// Typing indicator — best-effort, never blocks the reply flow.
	setPresence(ctx, in.Sender, res.Chat, types.ChatPresenceComposing, log)
	defer setPresence(ctx, in.Sender, res.Chat, types.ChatPresencePaused, log)

	start := time.Now()
	reply, err := in.Handler.Handle(ctx, res.Msg)
	elapsed := time.Since(start)
	if err != nil {
		log.Error("whatsapp.handler_failed",
			slog.String("err", err.Error()),
			slog.Duration("elapsed", elapsed))
		return
	}
	if reply == "" {
		log.Info("whatsapp.empty_reply", slog.Duration("elapsed", elapsed))
		return
	}
	log.Info("whatsapp.handler_ok",
		slog.Duration("elapsed", elapsed),
		slog.Int("reply_len", len(reply)))

	if err := in.Sender.SendText(ctx, res.Chat, PrependHeader(reply, in.Header)); err != nil {
		log.Error("whatsapp.send_failed", slog.String("err", err.Error()))
	}
}

func setPresence(ctx context.Context, s Sender, chat types.JID, state types.ChatPresence, log *slog.Logger) {
	if s == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.SendPresence(pctx, chat, state); err != nil {
		log.Debug("whatsapp.presence_failed",
			slog.String("state", string(state)),
			slog.String("err", err.Error()))
	}
}

func transcribeAudio(ctx context.Context, dl Downloader, t Transcriber, msg *waProto.AudioMessage, log *slog.Logger) (string, error) {
	if msg == nil {
		return "", errors.New("whatsapp: nil audio message")
	}
	start := time.Now()
	audio, mimetype, err := dl.Download(ctx, msg)
	if err != nil {
		return "", err
	}
	log.Info("whatsapp.audio_downloaded",
		slog.Int("bytes", len(audio)),
		slog.String("mimetype", mimetype),
		slog.Duration("elapsed", time.Since(start)))

	tstart := time.Now()
	text, err := t.Transcribe(ctx, audio, mimetype)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	log.Info("whatsapp.transcribed",
		slog.Int("chars", len(text)),
		slog.Duration("elapsed", time.Since(tstart)))
	return text, nil
}
