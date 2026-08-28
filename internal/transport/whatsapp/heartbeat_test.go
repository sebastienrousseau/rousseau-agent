//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
)

// heartbeatSender is fakeSender + MessageEditor. Records every send
// and every edit so tests can assert the full lifecycle.
type heartbeatSender struct {
	fakeSender
	mu          sync.Mutex
	sendWithID  []string // bodies passed to SendTextWithID
	sendIDErr   error
	edits       []editRecord
	editErr     error
	nextID      int
	editLatency time.Duration // if set, EditText sleeps this long before returning
}

type editRecord struct {
	ID   string
	Body string
}

func (e *heartbeatSender) SendTextWithID(_ context.Context, _ types.JID, body string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sendIDErr != nil {
		return "", e.sendIDErr
	}
	e.nextID++
	id := "wamid.HB" + itoa(e.nextID)
	e.sendWithID = append(e.sendWithID, body)
	return id, nil
}

func (e *heartbeatSender) EditText(_ context.Context, _ types.JID, id, body string) error {
	if e.editLatency > 0 {
		time.Sleep(e.editLatency)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.editErr != nil {
		return e.editErr
	}
	e.edits = append(e.edits, editRecord{ID: id, Body: body})
	return nil
}

func (e *heartbeatSender) editSnapshot() []editRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]editRecord, len(e.edits))
	copy(out, e.edits)
	return out
}

// itoa is a tiny int→string helper to avoid pulling strconv into
// this test file for a single call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestStartHeartbeat_NoEditorReturnsNil(t *testing.T) {
	// fakeSender does NOT implement MessageEditor — heartbeat must
	// no-op and let the caller fall through to the plain SendText
	// reply path.
	hb := startHeartbeat(context.Background(), &fakeSender{}, testChat(t), "", silentLogger())
	assert.Nil(t, hb)
	// nil-receiver-safe: subsequent finish/abort must not panic.
	assert.False(t, hb.finish(context.Background(), "reply"))
	hb.abort(context.Background())
}

func TestStartHeartbeat_SendFailureReturnsNil(t *testing.T) {
	ed := &heartbeatSender{sendIDErr: errors.New("wa: send")}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	assert.Nil(t, hb, "initial send failed → no heartbeat lifecycle")
}

func TestStartHeartbeat_SendsInitialPlaceholder(t *testing.T) {
	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "H: ", silentLogger())
	require.NotNil(t, hb)
	require.Len(t, ed.sendWithID, 1)
	assert.Equal(t, "H: ✻ working on it", ed.sendWithID[0])
	hb.abort(context.Background())
}

func TestHeartbeat_FinishReplacesPlaceholder(t *testing.T) {
	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)

	ok := hb.finish(context.Background(), "final reply text")
	assert.True(t, ok)

	// The last edit must be the final text; earlier edits (if any)
	// would be intermediate ticker refreshes — we did not wait long
	// enough for one to fire.
	edits := ed.editSnapshot()
	require.NotEmpty(t, edits)
	assert.Equal(t, "final reply text", edits[len(edits)-1].Body)
}

func TestHeartbeat_FinishReturnsFalseOnEditError(t *testing.T) {
	ed := &heartbeatSender{editErr: errors.New("edit rejected")}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)

	ok := hb.finish(context.Background(), "would-be reply")
	assert.False(t, ok, "edit failure → false so caller falls back to SendText")
}

func TestHeartbeat_AbortLeavesDoneMarker(t *testing.T) {
	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)

	hb.abort(context.Background())
	edits := ed.editSnapshot()
	require.NotEmpty(t, edits)
	assert.Equal(t, "● done", edits[len(edits)-1].Body, "abort must overwrite the placeholder with the done marker")
}

func TestHeartbeat_AbortSwallowsEditError(t *testing.T) {
	// abort must not panic or block when the edit fails; the reaction
	// is the load-bearing outcome signal.
	ed := &heartbeatSender{editErr: errors.New("network")}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)
	hb.abort(context.Background())
}

func TestHeartbeat_FinishStopsTickerGoroutine(t *testing.T) {
	// finish() must synchronise with the ticker goroutine's exit
	// (close(stopCh) → <-done). Otherwise a subsequent editSnapshot
	// could race an in-flight edit and the -race detector would trip.
	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)
	ok := hb.finish(context.Background(), "done")
	require.True(t, ok)
	// After finish, the ticker goroutine is guaranteed exited, so
	// snapshotting is race-free without additional synchronisation.
	edits := ed.editSnapshot()
	assert.NotEmpty(t, edits)
}

func TestHeartbeat_DoubleFinishSafe(t *testing.T) {
	// sync.Once inside heartbeat protects stopCh from a double-close.
	// Second finish() is a no-op that still returns true (the edit
	// succeeds on the underlying editor).
	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)
	assert.True(t, hb.finish(context.Background(), "first"))
	assert.True(t, hb.finish(context.Background(), "second"))
}

func TestHeartbeat_TickerEditsBeforeFinish(t *testing.T) {
	// Shrink the interval so a normal test runtime observes at least
	// one ticker-driven edit and the run() branch stops sitting at
	// 50% coverage.
	prev := heartbeatInterval
	heartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	ed := &heartbeatSender{}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)
	// Sleep long enough for at least a couple of ticker fires.
	time.Sleep(30 * time.Millisecond)
	require.True(t, hb.finish(context.Background(), "done"))

	edits := ed.editSnapshot()
	require.NotEmpty(t, edits)
	// One of the edits must be the ticker-generated spinner line
	// (`✻ <elapsed> · working on it`) with an elapsed time. Empty
	// Header defaults to DefaultReplyHeader, so match on body
	// substrings rather than a prefix.
	var sawTicker bool
	for _, e := range edits[:len(edits)-1] {
		if strings.Contains(e.Body, "✻ ") && strings.Contains(e.Body, "· working on it") {
			sawTicker = true
			break
		}
	}
	assert.True(t, sawTicker, "at least one interval-driven edit must fire before finish")
	// Last edit is the final body, as always.
	assert.Equal(t, "done", edits[len(edits)-1].Body)
}

func TestHeartbeat_TickerEditFailureIsLoggedNotFatal(t *testing.T) {
	// Ticker edit failures are best-effort — one failed edit should
	// log at debug level and the ticker keeps trying. This exercises
	// the log branch inside run().
	prev := heartbeatInterval
	heartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	ed := &heartbeatSender{editErr: errors.New("intermittent")}
	hb := startHeartbeat(context.Background(), ed, testChat(t), "", silentLogger())
	require.NotNil(t, hb)
	time.Sleep(30 * time.Millisecond)
	// finish also fails since editErr is on every EditText, but the
	// heartbeat must have survived the ticker's own edit failures.
	assert.False(t, hb.finish(context.Background(), "unused"))
}

func TestFormatHeartbeatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "1s"},
		{45 * time.Second, "45s"},
		{time.Minute, "1m"},
		{time.Minute + 30*time.Second, "1m30s"},
		{3 * time.Minute, "3m"},
		{4*time.Minute + 28*time.Second, "4m28s"},
	}
	for _, c := range cases {
		got := formatHeartbeatDuration(c.in)
		assert.Equal(t, c.want, got, c.in.String())
	}
}

// TestDispatch_HeartbeatUpgradedInPlaceToReply drives the end-to-end
// happy path through Dispatch: the reply lands as an EDIT to the
// heartbeat placeholder, not a fresh SendText. The plain-sent
// message list stays empty because the edit path handled it.
func TestDispatch_HeartbeatUpgradedInPlaceToReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.HBUP"

	send := &heartbeatSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("hello back", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})

	// Initial heartbeat placeholder sent. Header=" " (single space)
	// is the "disable prefix" sentinel per PrependHeader's contract,
	// so the placeholder body is the bare glyph + label.
	require.Len(t, send.sendWithID, 1)
	assert.Equal(t, "✻ working on it", send.sendWithID[0])
	// Reply landed as an edit, not a new send.
	assert.Empty(t, send.sent, "reply must be delivered via edit, not SendText")
	edits := send.editSnapshot()
	require.NotEmpty(t, edits)
	assert.Equal(t, "hello back", edits[len(edits)-1].Body)
}

// TestDispatch_HeartbeatAbortedOnHandlerError verifies the failure
// path swaps the placeholder for the done marker and does NOT send a
// separate reply message.
func TestDispatch_HeartbeatAbortedOnHandlerError(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.HBERR"

	send := &heartbeatSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", errors.New("boom")),
		Logger:  silentLogger(),
	})

	assert.Empty(t, send.sent, "handler error must not push a fresh reply")
	edits := send.editSnapshot()
	require.NotEmpty(t, edits)
	assert.Equal(t, "● done", edits[len(edits)-1].Body)
}

// TestDispatch_HeartbeatAbortedOnEmptyReply — same shape, empty
// (not errored) reply. Emoji reaction alone would leave the
// "✻ working on it" placeholder in the thread; abort() rewrites it
// to a short "done" marker.
func TestDispatch_HeartbeatAbortedOnEmptyReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.HBEMPTY"

	send := &heartbeatSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", nil),
		Logger:  silentLogger(),
	})

	assert.Empty(t, send.sent)
	edits := send.editSnapshot()
	require.NotEmpty(t, edits)
	assert.Equal(t, "● done", edits[len(edits)-1].Body)
}

// TestDispatch_HeartbeatFinishFailureFallsBackToSendText — when the
// final edit call fails (typically because WhatsApp's 15-min edit
// window closed on a very long turn), the reply is still delivered
// via a fresh SendText. The user sees BOTH the placeholder (frozen
// as "✻ working on it") AND the reply; a lingering placeholder is a
// worse UX than a duplicated message.
func TestDispatch_HeartbeatFinishFailureFallsBackToSendText(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.HBFAIL"

	// editErr fires on EVERY EditText — including finish's final edit.
	send := &heartbeatSender{editErr: errors.New("edit-window closed")}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("late reply", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})

	require.Len(t, send.sendWithID, 1, "the placeholder still went out")
	require.Len(t, send.sent, 1, "edit failure → fresh SendText fallback")
	assert.Equal(t, "late reply", send.sent[0])
}

// TestDispatch_HeartbeatFallsBackWhenSenderLacksEditor — the pre-
// heartbeat behaviour must survive when a Sender does not implement
// MessageEditor (existing test fakes). No placeholder, reply via
// SendText, done.
func TestDispatch_HeartbeatFallsBackWhenSenderLacksEditor(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")

	send := &fakeSender{} // no MessageEditor
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("plain reply", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})

	require.Len(t, send.sent, 1)
	assert.Equal(t, "plain reply", send.sent[0])
}
