//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// plainSender implements Sender only. It stands in for a transport
// that cannot edit, which is what forces the fallback sink.
type plainSender struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (p *plainSender) SendText(_ context.Context, _ types.JID, body string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, body)
	return p.err
}

func (p *plainSender) SendPresence(context.Context, types.JID, types.ChatPresence) error {
	return nil
}

// editingSender also implements MessageEditor, which is what unlocks
// in-place editing and the faster refresh interval.
type editingSender struct {
	plainSender
	mu       sync.Mutex
	edits    []string
	ids      []string
	nextID   string
	sendErr  error
	editErr  error
	editedTo types.JID
}

func (e *editingSender) SendTextWithID(_ context.Context, _ types.JID, body string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids = append(e.ids, body)
	if e.nextID == "" {
		e.nextID = "msg-1"
	}
	return e.nextID, e.sendErr
}

func (e *editingSender) EditText(_ context.Context, chat types.JID, id, body string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.editedTo = chat
	e.edits = append(e.edits, id+":"+body)
	return e.editErr
}

func testChat(t *testing.T) types.JID {
	t.Helper()
	jid, err := types.ParseJID("15551234567@s.whatsapp.net")
	require.NoError(t, err)
	return jid
}

func TestNewProgressSink_PrefersTheEditingSink(t *testing.T) {
	chat := testChat(t)

	t.Run("editor available", func(t *testing.T) {
		s := newProgressSink(&editingSender{}, chat)
		_, isEditor := s.(progress.Editor)
		assert.True(t, isEditor,
			"a sender that can edit must yield a sink the Reporter can edit through")
	})

	t.Run("editor absent", func(t *testing.T) {
		s := newProgressSink(&plainSender{}, chat)
		_, isEditor := s.(progress.Editor)
		assert.False(t, isEditor,
			"a plain sender must not advertise editing it cannot do")
	})
}

func TestProgressSink_SendUsesSendTextWithIDWhenAvailable(t *testing.T) {
	snd := &editingSender{nextID: "wamid-42"}
	s := newProgressSink(snd, testChat(t))

	h, err := s.Send(context.Background(), progress.Update{Text: "working…"})
	require.NoError(t, err)
	assert.Equal(t, progress.Handle("wamid-42"), h,
		"the returned handle must be the message id, or later edits target nothing")
	assert.Equal(t, []string{"working…"}, snd.ids)
	assert.Empty(t, snd.sent, "the editing path must not fall back to SendText")
}

func TestProgressSink_SendFallsBackToPlainSend(t *testing.T) {
	snd := &plainSender{}
	s := newProgressSink(snd, testChat(t))

	h, err := s.Send(context.Background(), progress.Update{Text: "working…"})
	require.NoError(t, err)
	assert.Empty(t, h, "a sender that cannot edit has no handle to give back")
	assert.Equal(t, []string{"working…"}, snd.sent)
}

func TestProgressSink_SendPropagatesErrors(t *testing.T) {
	want := errors.New("network down")

	t.Run("editing path", func(t *testing.T) {
		s := newProgressSink(&editingSender{sendErr: want}, testChat(t))
		_, err := s.Send(context.Background(), progress.Update{Text: "x"})
		assert.ErrorIs(t, err, want)
	})

	t.Run("plain path", func(t *testing.T) {
		s := newProgressSink(&plainSender{err: want}, testChat(t))
		_, err := s.Send(context.Background(), progress.Update{Text: "x"})
		assert.ErrorIs(t, err, want)
	})
}

func TestEditingProgressSink_EditTargetsTheHandleAndChat(t *testing.T) {
	snd := &editingSender{}
	chat := testChat(t)
	s := newProgressSink(snd, chat)
	ed, ok := s.(progress.Editor)
	require.True(t, ok)

	require.NoError(t, ed.Edit(context.Background(), progress.Handle("wamid-7"),
		progress.Update{Text: "still working…"}))
	assert.Equal(t, []string{"wamid-7:still working…"}, snd.edits)
	assert.Equal(t, chat, snd.editedTo, "edits must go to the same chat")
}

func TestEditingProgressSink_EditPropagatesError(t *testing.T) {
	want := errors.New("edit rejected")
	s := newProgressSink(&editingSender{editErr: want}, testChat(t))
	ed := s.(progress.Editor) //nolint:errcheck // asserted by the sibling test
	assert.ErrorIs(t, ed.Edit(context.Background(), "h", progress.Update{Text: "x"}), want)
}

func TestStartProgress_ReturnsNilWhenNothingToReportThrough(t *testing.T) {
	// Callers nil-check the returned stop func rather than branching on
	// three conditions, so each of these must yield nil.
	chat := testChat(t)
	bus := progress.NewBus(progress.BusOptions{})
	t.Cleanup(bus.Close)

	assert.Nil(t, startProgress(context.Background(), nil, &plainSender{}, chat, "k", nil),
		"no bus means nothing to subscribe to")
	assert.Nil(t, startProgress(context.Background(), bus, nil, chat, "k", nil),
		"no sender means nowhere to deliver")
	assert.Nil(t, startProgress(context.Background(), bus, &plainSender{}, chat, "", nil),
		"no routing key means updates cannot be attributed to a turn")
}

func TestStartProgress_StopDetachesAndWaits(t *testing.T) {
	chat := testChat(t)
	bus := progress.NewBus(progress.BusOptions{})
	t.Cleanup(bus.Close)

	stop := startProgress(context.Background(), bus, &editingSender{}, chat, "k",
		slog.New(slog.DiscardHandler))
	require.NotNil(t, stop)

	// stop must block until the reporter goroutine has exited, so a
	// caller that returns immediately afterwards cannot leak it.
	stop()
}
