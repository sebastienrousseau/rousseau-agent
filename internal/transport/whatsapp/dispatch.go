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

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
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
	// Progress, when non-nil, is subscribed for the duration of the
	// handler call so the user sees live updates while a long turn
	// runs. Always populated by Client; nil only in unit tests that
	// exercise the plain reply path.
	Progress *progress.Bus
	// IsAllowed, when non-nil, is consulted BEFORE any user-visible
	// action for an inbound message (emoji reactions, typing
	// indicators, placeholder). A false verdict silently drops the
	// message — the sender sees nothing, no signal that a bot is
	// watching the number. Nil means "no pre-filter" — sensible for
	// unit tests, dangerous in prod (privacy leak: the ✅ ack tells
	// strangers the number is bot-monitored).
	IsAllowed func(from string) bool
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
	// Pre-reaction allowlist gate. Runs BEFORE the receipt emoji so a
	// stranger who happens across the number sees nothing back — no
	// signal that a bot is watching. Without this the ✅ (empty-reply
	// path) and 👀 (immediate ack) both leaked to non-allowlisted
	// senders because the daemon-side Router silently returns ("", nil)
	// for rejected messages, which dispatch treated as an ordinary
	// empty reply and acked with ✅.
	if in.IsAllowed != nil && !in.IsAllowed(res.Msg.From) {
		log.Info("whatsapp.dropped_pre_reaction", slog.String("from", res.Msg.From))
		return
	}
	log.Info("whatsapp.incoming", slog.String("from", res.Msg.From))

	// Immediate emoji-reaction ack: sub-second confirmation of receipt,
	// no chat clutter. Best-effort; if the sender does not implement
	// Reactor (test fakes) or the send fails, we silently continue.
	react := reactorFor(in)
	react(ctx, "👀")

	// Live placeholder + progress feed. Two exclusive sources of the
	// "working" placeholder exist; when both are available they would
	// send two separate messages and fight over edits, which reads as
	// the bot editing itself repeatedly:
	//
	//   * tier-3 progress feed (startProgress, when in.Progress is set):
	//     sends a placeholder on first bus event and edits it with the
	//     Claude-CLI-style bullet feed. Its own terminal render is the
	//     "● done in Ns · N tools" summary.
	//   * tier-1 heartbeat (startHeartbeat): sends a plain "✻ working
	//     on it" placeholder and edits the elapsed-time counter every
	//     15s. Only meaningful when there is no tier-3 feed to do
	//     something better.
	//
	// Precedence: whichever is available. If both are available the
	// tier-3 feed wins — its output is strictly more informative and
	// its lifetime already spans the turn.
	var hb *heartbeat
	if in.Progress == nil {
		hb = startHeartbeat(ctx, in.Sender, res.Chat, in.Header, log)
	}

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
		hb.abort(context.Background())
		react(context.Background(), "❌")
		return
	}
	if reply == "" {
		log.Info("whatsapp.empty_reply", slog.Duration("elapsed", elapsed))
		hb.abort(context.Background())
		react(context.Background(), "✅")
		return
	}
	log.Info("whatsapp.handler_ok",
		slog.Duration("elapsed", elapsed),
		slog.Int("reply_len", len(reply)))

	// The reply is always its own message, never an edit of a
	// placeholder. Reasons:
	//
	//   * Editing loses the running placeholder from the thread
	//     history, which is where the user reads back what the bot
	//     actually did during the turn (bullets or elapsed counter).
	//   * Edits do not push a notification, so an edited reply looks
	//     to the user like the bot went silent.
	//   * The user's mental model is "each message from the bot is a
	//     new message" — matching WhatsApp's normal chat pattern
	//     rather than a live-updating widget.
	//
	// The placeholder is closed independently: tier-1 heartbeat's
	// abort() leaves a "● done" marker; the tier-3 progress feed
	// emits its own terminal render ("● done in Ns · N tools") via
	// the bus when the turn's Publisher fires KindTurnFinished.
	hb.abort(context.Background())
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
