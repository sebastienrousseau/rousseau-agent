//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// heartbeatInterval is how often the placeholder message is edited
// to update the elapsed-time counter. 15s balances "obviously alive"
// against WhatsApp's rate limit and battery use on the user's phone
// (each edit is a push notification suppressed by the platform, but
// still a wire event).
//
// Kept as a var (not const) so tests can shorten it to a few
// milliseconds and observe an interval-driven edit without a real
// 15-second wait.
var heartbeatInterval = 15 * time.Second

// heartbeatInitial is the body sent immediately after the emoji-ack
// reaction. It sits in the thread until the turn finishes; the
// ticker rewrites it with a live elapsed-time counter each interval.
const heartbeatInitial = "🟡 working…"

// heartbeatFinal is the fallback body used when abort() runs — a
// short, unambiguous marker so a stopped placeholder does not linger
// as "working…".
const heartbeatFinal = "✅ done"

// heartbeatEditTimeout bounds every edit call so a slow network
// cannot wedge the ticker goroutine or the finish path.
const heartbeatEditTimeout = 5 * time.Second

// heartbeat is a live placeholder message in the WhatsApp thread. It
// gives the user something to look at while a long turn runs: sent
// once, edited in place every heartbeatInterval with the elapsed
// counter, then replaced with the final reply text.
//
// The design uses WhatsApp's message-edit primitive rather than a
// stream of new messages because edits do not push a notification to
// the user's phone; a stream of new messages would light up the
// notification tray every 15 seconds.
type heartbeat struct {
	ed     MessageEditor
	chat   types.JID
	msgID  string
	log    *slog.Logger
	start  time.Time
	stopCh chan struct{}
	done   chan struct{}
	once   sync.Once
}

// startHeartbeat sends the placeholder and spawns the refresh loop.
// Returns nil (no-op) when the sender does not implement
// MessageEditor — test fakes typically do not, and the caller then
// falls back to the old "send a fresh message when the reply is
// ready" path.
//
// The initial send failure is also treated as "no heartbeat": no
// point trying to edit a message that never landed.
func startHeartbeat(ctx context.Context, s Sender, chat types.JID, header string, log *slog.Logger) *heartbeat {
	ed, ok := s.(MessageEditor)
	if !ok {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, heartbeatEditTimeout)
	defer cancel()
	id, err := ed.SendTextWithID(sctx, chat, PrependHeader(heartbeatInitial, header))
	if err != nil {
		log.Debug("whatsapp.heartbeat_send_failed", slog.String("err", err.Error()))
		return nil
	}
	hb := &heartbeat{
		ed:     ed,
		chat:   chat,
		msgID:  id,
		log:    log,
		start:  time.Now(),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go hb.run(header)
	return hb
}

// run edits the placeholder every heartbeatInterval with a fresh
// elapsed-time counter until stop signals the loop to exit. Best-
// effort: an edit failure is logged and skipped, not fatal.
func (h *heartbeat) run(header string) {
	defer close(h.done)
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case now := <-t.C:
			elapsed := now.Sub(h.start)
			body := PrependHeader(fmt.Sprintf("🟡 working… %s", formatHeartbeatDuration(elapsed)), header)
			ectx, cancel := context.WithTimeout(context.Background(), heartbeatEditTimeout)
			if err := h.ed.EditText(ectx, h.chat, h.msgID, body); err != nil {
				h.log.Debug("whatsapp.heartbeat_edit_failed", slog.String("err", err.Error()))
			}
			cancel()
		}
	}
}

// finish stops the refresh loop and replaces the placeholder with
// finalText (already header-prefixed by the caller). Returns true
// when the edit succeeded — the caller can then skip sending a
// separate reply message, keeping the thread to one placeholder-
// upgraded-in-place message per turn.
//
// Nil receiver is safe: it returns false so the caller falls back to
// the plain SendText path (which is what a Sender without
// MessageEditor would have used anyway).
func (h *heartbeat) finish(ctx context.Context, finalText string) bool {
	if h == nil {
		return false
	}
	h.once.Do(func() { close(h.stopCh) })
	<-h.done
	ectx, cancel := context.WithTimeout(ctx, heartbeatEditTimeout)
	defer cancel()
	if err := h.ed.EditText(ectx, h.chat, h.msgID, finalText); err != nil {
		h.log.Debug("whatsapp.heartbeat_finish_failed", slog.String("err", err.Error()))
		return false
	}
	return true
}

// abort stops the refresh loop and replaces the placeholder with a
// short "done" marker. Used when the handler returned an empty reply
// or an error and there is no meaningful reply text to insert; the
// emoji reaction on the user's message carries the outcome, and a
// lingering "working…" placeholder would misinform.
func (h *heartbeat) abort(ctx context.Context) {
	if h == nil {
		return
	}
	h.once.Do(func() { close(h.stopCh) })
	<-h.done
	ectx, cancel := context.WithTimeout(ctx, heartbeatEditTimeout)
	defer cancel()
	if err := h.ed.EditText(ectx, h.chat, h.msgID, heartbeatFinal); err != nil {
		h.log.Debug("whatsapp.heartbeat_abort_failed", slog.String("err", err.Error()))
	}
}

// formatHeartbeatDuration renders elapsed as e.g. "45s", "1m30s",
// "3m", suitable for a live status line. Rounds to whole seconds so
// the counter increments monotonically instead of jittering on
// sub-second boundaries.
func formatHeartbeatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int((d - time.Duration(m)*time.Minute).Seconds())
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}
