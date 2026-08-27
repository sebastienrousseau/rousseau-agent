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

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Downloader downloads media attached to a whatsmeow message. Extracted
// so tests can inject a fixture-returning fake and exercise media
// handling without a live client. The interface is deliberately
// media-neutral: audio (voice notes) and image (photo) messages both
// travel through the same Download call.
type Downloader interface {
	// Download fetches the media payload for a downloadable message.
	// mimetype is the envelope-reported value from whatsmeow — never
	// trusted downstream, image path re-sniffs from bytes.
	Download(ctx context.Context, msg Downloadable) (bytes []byte, mimetype string, err error)
}

// Downloadable is the smallest shape a whatsmeow media message needs
// to expose for our Downloader. Every *waProto.{Audio,Image,Video,…}
// Message type satisfies it, so widening this later means a bigger
// switch inside Dispatch, not a signature change.
type Downloadable interface {
	GetMimetype() string
}

// DownloadableAudio narrows Downloadable for the audio branch, which
// needs the duration for log/ignore decisions. Only accepted by
// transcribeAudio, not by Downloader itself.
type DownloadableAudio interface {
	Downloadable
	GetSeconds() uint32
}

// DownloadableImage narrows Downloadable for the image branch. The
// dimension getters are used to log which image the pipeline handled
// and to reject decode-time size explosions before they hit the
// model.
type DownloadableImage interface {
	Downloadable
	GetWidth() uint32
	GetHeight() uint32
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
	// Progress, when non-nil, is subscribed for the duration of the
	// handler call so the user sees live updates while a long turn
	// runs. Always populated by Client; nil only in unit tests that
	// exercise the plain reply path.
	Progress *progress.Bus
	// MediaPolicy governs which images are accepted (MIME allowlist,
	// per-image and per-turn byte caps). Zero-value falls back to the
	// media.Policy defaults documented on that package.
	MediaPolicy media.Policy
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

	// Images share the audio path's SkipEmptyText recovery: a
	// photo-only message with no caption still deserves the model's
	// attention. The image itself becomes an Attachment on the
	// IncomingMessage; the caption (if any) fills Body.
	if imageMsg := in.Event.Message.GetImageMessage(); imageMsg != nil && res.Skip == SkipEmptyText {
		if in.Downloader == nil {
			log.Info("whatsapp.image_ignored",
				slog.String("reason", "downloader_not_configured"))
			return
		}
		att, err := downloadImage(ctx, in.Downloader, imageMsg, in.MediaPolicy, log)
		if err != nil {
			log.Error("whatsapp.image_failed", slog.String("err", err.Error()))
			return
		}
		if att == nil {
			// Dropped by policy (size cap, disallowed MIME); already logged.
			return
		}
		res = Resolved{
			Msg: transport.IncomingMessage{
				From:        resolveFrom(in.Event, in.OwnID).String(),
				Body:        imageMsg.GetCaption(),
				At:          in.Event.Info.Timestamp,
				Attachments: []transport.Attachment{*att},
			},
			Chat: in.Event.Info.Chat,
		}
		handleTextMessage(ctx, in, res, log)
		return
	}

	if res.Skip == SkipOwnOutbound {
		// Visible at INFO because this is the safety-critical case: an
		// outbound-from-us echo that the agent must not answer. Logging
		// Chat and Sender lets an operator confirm the guard is firing
		// on the right shape of event.
		log.Info("whatsapp.skipped_own_outbound",
			slog.String("chat", in.Event.Info.Chat.String()),
			slog.String("sender", in.Event.Info.Sender.String()))
		return
	}
	if res.Skip != SkipEmptyText {
		log.Debug("whatsapp.skipped", slog.String("reason", string(res.Skip)))
	}
}

// resolveFrom mirrors the sender-normalisation in ResolveInbound. It
// is used by the audio-transcription branch, which constructs its own
// Resolved after downloading media. Must apply the same PN-preference
// logic so voice notes route through the same allowlist path as text.
func resolveFrom(evt *events.Message, ownID *types.JID) types.JID {
	from := preferPN(evt.Info.Sender, evt.Info.SenderAlt).ToNonAD()
	if evt.Info.IsFromMe && ownID != nil {
		from = ownID.ToNonAD()
	}
	return from
}

func handleTextMessage(ctx context.Context, in DispatchInput, res Resolved, log *slog.Logger) {
	log.Info("whatsapp.incoming", slog.String("from", res.Msg.From))

	// Immediate emoji-reaction ack: sub-second confirmation of receipt,
	// no chat clutter. Best-effort; if the sender does not implement
	// Reactor (test fakes) or the send fails, we silently continue.
	react := reactorFor(in)
	react(ctx, "👀")

	// Typing indicator — best-effort, never blocks the reply flow.
	setPresence(ctx, in.Sender, res.Chat, types.ChatPresenceComposing, log)
	defer setPresence(ctx, in.Sender, res.Chat, types.ChatPresencePaused, log)

	// Live progress for the duration of the turn. The routing key is
	// the sender identifier, which is exactly what the control
	// Supervisor keys its registry on, so the two halves meet without
	// either learning about session IDs.
	if stop := startProgress(ctx, in.Progress, in.Sender, res.Chat, res.Msg.From, log); stop != nil {
		defer stop()
	}

	start := time.Now()
	reply, err := in.Handler.Handle(ctx, res.Msg)
	elapsed := time.Since(start)
	if err != nil {
		log.Error("whatsapp.handler_failed",
			slog.String("err", err.Error()),
			slog.Duration("elapsed", elapsed))
		react(context.Background(), "❌")
		return
	}
	if reply == "" {
		log.Info("whatsapp.empty_reply", slog.Duration("elapsed", elapsed))
		react(context.Background(), "✅")
		return
	}
	log.Info("whatsapp.handler_ok",
		slog.Duration("elapsed", elapsed),
		slog.Int("reply_len", len(reply)))

	if err := in.Sender.SendText(ctx, res.Chat, PrependHeader(reply, in.Header)); err != nil {
		log.Error("whatsapp.send_failed", slog.String("err", err.Error()))
		react(context.Background(), "❌")
		return
	}
	react(context.Background(), "✅")
}

// reactionTimeout bounds each reaction send so a slow network never
// blocks the reply flow. Reactions are best-effort UX; missing one is
// far cheaper than a stuck handler.
const reactionTimeout = 3 * time.Second

// reactorFor returns a closure that reacts to the current inbound
// event when the sender supports it. When the sender does not
// implement Reactor (unit fakes) or in.Event lacks an ID, the closure
// is a no-op — callers do not branch.
func reactorFor(in DispatchInput) func(context.Context, string) {
	r, ok := in.Sender.(Reactor)
	if !ok || in.Event == nil || in.Event.Info.ID == "" {
		return func(context.Context, string) {}
	}
	chat := in.Event.Info.Chat
	senderJID := in.Event.Info.Sender
	targetID := in.Event.Info.ID
	log := in.Logger
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, emoji string) {
		rctx, cancel := context.WithTimeout(ctx, reactionTimeout)
		defer cancel()
		if err := r.React(rctx, chat, senderJID, targetID, emoji); err != nil {
			log.Debug("whatsapp.react_failed",
				slog.String("emoji", emoji),
				slog.String("err", err.Error()))
		}
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

// downloadImage fetches an inbound image via dl, sniffs the MIME from
// the payload bytes (never the envelope), and applies policy. Returns
// (nil, nil) when the image is dropped by policy — the caller should
// stop processing quietly, the reason is already logged. Non-nil err
// signals a fetch failure worth surfacing.
func downloadImage(ctx context.Context, dl Downloader, msg *waProto.ImageMessage, policy media.Policy, log *slog.Logger) (*transport.Attachment, error) {
	if msg == nil {
		return nil, errors.New("whatsapp: nil image message")
	}
	start := time.Now()
	data, envelopeMIME, err := dl.Download(ctx, msg)
	if err != nil {
		return nil, err
	}
	log.Info("whatsapp.image_downloaded",
		slog.Int("bytes", len(data)),
		slog.String("envelope_mime", envelopeMIME),
		slog.Uint64("width", uint64(msg.GetWidth())),
		slog.Uint64("height", uint64(msg.GetHeight())),
		slog.Duration("elapsed", time.Since(start)))

	sniffed, err := policy.Accept(data, 0)
	if err != nil {
		log.Info("whatsapp.image_dropped",
			slog.String("reason", err.Error()),
			slog.String("envelope_mime", envelopeMIME),
			slog.Int("bytes", len(data)))
		return nil, nil
	}
	if sniffed != envelopeMIME {
		// Envelope disagreed with the actual bytes. Not fatal — we
		// use the sniffed value — but worth an operator-visible log.
		log.Warn("whatsapp.image_mime_lied",
			slog.String("envelope", envelopeMIME),
			slog.String("sniffed", sniffed))
	}
	return &transport.Attachment{MediaType: sniffed, Data: data}, nil
}
