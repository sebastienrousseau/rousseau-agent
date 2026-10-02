package transport_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestRouter_SameIDOnTwoTransportsStaysSeparate pins sender-key
// namespacing: one phone number on Signal and on iMessage, sharing one
// store, gets two sessions, and neither transport's /find or
// /sessions sees the other's conversation.
func TestRouter_SameIDOnTwoTransportsStaysSeparate(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)
	mk := func(tp string) *transport.Router {
		return transport.NewRouter(&staticRunner{reply: "ok"}, &storeAdapter{s: store}, jm, silent(),
			transport.RouterOptions{Transport: tp})
	}
	signal, imessage := mk("signal"), mk("imessage")
	const number = "+447700900123"

	_, err = signal.Handle(ctx, transport.IncomingMessage{From: number, Body: "my signal secret pineapple"})
	require.NoError(t, err)
	_, err = imessage.Handle(ctx, transport.IncomingMessage{From: number, Body: "hello from imessage"})
	require.NoError(t, err)

	for _, tp := range []string{"signal", "imessage"} {
		got, err := store.ListBySender(ctx, tp+":"+number, 0)
		require.NoError(t, err)
		assert.Len(t, got, 1, "%s has its own session", tp)
	}
	found, err := signal.Handle(ctx, transport.IncomingMessage{From: number, Body: "/find pineapple"})
	require.NoError(t, err)
	assert.NotContains(t, found, "no matches")
	leaked, err := imessage.Handle(ctx, transport.IncomingMessage{From: number, Body: "/find pineapple"})
	require.NoError(t, err)
	assert.Contains(t, leaked, "no matches", "iMessage must not find the Signal conversation")
}
