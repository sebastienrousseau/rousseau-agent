//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
)

// reactingSender is fakeSender + Reactor. Existing tests keep using
// fakeSender (which does NOT satisfy Reactor) to prove the dispatch
// path stays silent when the sender lacks the capability.
type reactingSender struct {
	fakeSender
	mu        sync.Mutex
	reactions []reactCall
	reactErr  error
}

type reactCall struct {
	Chat      types.JID
	SenderJID types.JID
	TargetID  string
	Emoji     string
}

func (r *reactingSender) React(_ context.Context, chat, senderJID types.JID, targetID, emoji string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reactErr != nil {
		return r.reactErr
	}
	r.reactions = append(r.reactions, reactCall{Chat: chat, SenderJID: senderJID, TargetID: targetID, Emoji: emoji})
	return nil
}

func (r *reactingSender) snapshot() []reactCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reactCall, len(r.reactions))
	copy(out, r.reactions)
	return out
}

func TestDispatch_ReactsOnReceiptAndSuccess(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.ABC123"

	send := &reactingSender{}
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
	reactions := send.snapshot()
	require.Len(t, reactions, 2, "expected receipt + completion reaction")
	assert.Equal(t, "👀", reactions[0].Emoji)
	assert.Equal(t, "✅", reactions[1].Emoji)
	assert.Equal(t, "wamid.ABC123", reactions[0].TargetID)
	assert.Equal(t, sender, reactions[0].SenderJID)
	assert.Equal(t, sender.ToNonAD(), reactions[0].Chat)
}

func TestDispatch_ReactsWithFailureOnHandlerError(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.ERR"

	send := &reactingSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", errors.New("boom")),
		Logger:  silentLogger(),
	})

	assert.Empty(t, send.sent)
	reactions := send.snapshot()
	require.Len(t, reactions, 2)
	assert.Equal(t, "👀", reactions[0].Emoji)
	assert.Equal(t, "❌", reactions[1].Emoji)
}

func TestDispatch_ReactsWithFailureOnSendError(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.SEND"

	send := &reactingSender{}
	send.sendErr = errors.New("send failed")
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("body", nil),
		Logger:  silentLogger(),
	})

	reactions := send.snapshot()
	require.Len(t, reactions, 2)
	assert.Equal(t, "👀", reactions[0].Emoji)
	assert.Equal(t, "❌", reactions[1].Emoji)
}

func TestDispatch_ReactsWithSuccessOnEmptyReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.EMPTY"

	send := &reactingSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("", nil),
		Logger:  silentLogger(),
	})

	reactions := send.snapshot()
	require.Len(t, reactions, 2)
	assert.Equal(t, "👀", reactions[0].Emoji)
	assert.Equal(t, "✅", reactions[1].Emoji)
}

func TestDispatch_ReactorlessSenderSilent(t *testing.T) {
	// The existing fakeSender in dispatch_test.go does not implement
	// Reactor. Dispatch must not panic and must not attempt reactions.
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.NORX"

	send := &fakeSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("body", nil),
		Logger:  silentLogger(),
	})
	require.Len(t, send.sent, 1)
}

func TestDispatch_SkipsReactionWhenEventIDMissing(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	// Deliberately leave Info.ID empty.

	send := &reactingSender{}
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("body", nil),
		Logger:  silentLogger(),
	})

	require.Len(t, send.sent, 1)
	assert.Empty(t, send.snapshot(), "no event ID means no reaction can be sent")
}

func TestDispatch_ReactErrorDoesNotBlockReply(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "wamid.RXERR"

	send := &reactingSender{}
	send.reactErr = errors.New("react blocked")
	Dispatch(context.Background(), DispatchInput{
		Event:   evt,
		OwnID:   &own,
		Sender:  send,
		Handler: handlerReturning("still delivered", nil),
		Header:  " ",
		Logger:  silentLogger(),
	})

	require.Len(t, send.sent, 1)
	assert.Equal(t, "still delivered", send.sent[0])
}
