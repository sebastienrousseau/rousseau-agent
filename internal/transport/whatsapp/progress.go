//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"log/slog"

	"go.mau.fi/whatsmeow/types"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// progressSendTimeout is not enforced here on purpose: the Reporter
// hands us the turn's context, and a progress send that outlives the
// turn is pointless. Sinks simply inherit that deadline.

// progressSink delivers coalesced progress updates to one WhatsApp
// chat.
//
// Progress messages deliberately carry NO reply header. The header
// marks answers; repeating it on a status line that may be refreshed
// twenty times would make the thread unreadable.
type progressSink struct {
	sender Sender
	chat   types.JID
}

// Send satisfies progress.Sink.
func (s *progressSink) Send(ctx context.Context, u progress.Update) (progress.Handle, error) {
	if ed, ok := s.sender.(MessageEditor); ok {
		id, err := ed.SendTextWithID(ctx, s.chat, u.Text)
		return progress.Handle(id), err
	}
	return "", s.sender.SendText(ctx, s.chat, u.Text)
}

// editingProgressSink adds in-place editing for senders that support
// it. The Reporter type-asserts for progress.Editor, so the two sink
// shapes are what decide whether the throttle runs at MinEditInterval
// (10s, silent) or MinInterval (25s, a notification each time).
type editingProgressSink struct {
	progressSink
	editor MessageEditor
}

// Edit satisfies progress.Editor.
func (s *editingProgressSink) Edit(ctx context.Context, h progress.Handle, u progress.Update) error {
	return s.editor.EditText(ctx, s.chat, string(h), u.Text)
}

// newProgressSink returns the richest sink the sender supports.
func newProgressSink(sender Sender, chat types.JID) progress.Sink {
	base := progressSink{sender: sender, chat: chat}
	if ed, ok := sender.(MessageEditor); ok {
		return &editingProgressSink{progressSink: base, editor: ed}
	}
	return &base
}

// startProgress subscribes to bus for key and pumps coalesced updates
// into chat for the duration of one turn. The returned stop function
// detaches the subscription and waits for the reporter goroutine to
// exit; it is safe to call exactly once.
//
// Returns nil when there is nothing to report through, so callers can
// nil-check instead of branching on three separate conditions.
func startProgress(ctx context.Context, bus *progress.Bus, sender Sender, chat types.JID, key string, logger *slog.Logger) func() {
	if bus == nil || sender == nil || key == "" {
		return nil
	}
	sub := bus.Subscribe(key)
	rep := progress.NewReporter(progress.ReporterConfig{
		Sub:    sub,
		Sink:   newProgressSink(sender, chat),
		Logger: logger,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rep.Run(ctx)
	}()
	return func() {
		sub.Close()
		<-done
	}
}
