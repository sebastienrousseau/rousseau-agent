package transport_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// twoRouters wires a whatsapp and a slack router over one identity
// store and one set of pending link codes, as the daemon does.
func twoRouters(t *testing.T) (wa, sl *transport.Router, ids *sqlitestore.IdentityStore, ctx context.Context) {
	t.Helper()
	ctx = context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)
	ids, err = sqlitestore.NewIdentityStore(ctx, store)
	require.NoError(t, err)
	codes := transport.NewLinkCodes()
	mk := func(name string) *transport.Router {
		return transport.NewRouter(&staticRunner{reply: "ok"}, &storeAdapter{s: store}, jm, silent(),
			transport.RouterOptions{Identity: ids, Transport: name, LinkCodes: codes})
	}
	return mk("whatsapp"), mk("slack"), ids, ctx
}

var confirmCode = regexp.MustCompile(`/confirm (\d{6})`)

func say(t *testing.T, r *transport.Router, ctx context.Context, from, body string) string {
	t.Helper()
	reply, err := r.Handle(ctx, transport.IncomingMessage{From: from, Body: body})
	require.NoError(t, err)
	return reply
}

func TestUnlink_OtherIdentityRefused(t *testing.T) {
	wa, _, ids, ctx := twoRouters(t)
	say(t, wa, ctx, "+alice", "/whoami")
	say(t, wa, ctx, "+mallory", "/whoami")
	reply := say(t, wa, ctx, "+mallory", "/unlink whatsapp:+alice")
	assert.Contains(t, reply, "not linked to your identity")
	_, err := ids.Resolve(ctx, "whatsapp", "+alice")
	assert.NoError(t, err, "another identity's handle stays linked")
}

func TestLink_RequiresConfirmationFromTarget(t *testing.T) {
	wa, sl, ids, ctx := twoRouters(t)
	reply := say(t, wa, ctx, "+alice", "/link slack:UALICE")
	m := confirmCode.FindStringSubmatch(reply)
	require.Len(t, m, 2, "the reply tells the user what to send from the target handle")
	_, err := ids.Resolve(ctx, "slack", "UALICE")
	assert.Error(t, err, "nothing is linked before the target confirms")

	assert.Contains(t, say(t, sl, ctx, "UMALLORY", "/confirm "+m[1]), "no pending link")
	reply = say(t, sl, ctx, "UALICE", "/confirm "+m[1])
	assert.Contains(t, reply, "linked slack:UALICE")
	a, err := ids.Resolve(ctx, "whatsapp", "+alice")
	require.NoError(t, err)
	b, err := ids.Resolve(ctx, "slack", "UALICE")
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestLink_WrongCodeCancelsPending(t *testing.T) {
	wa, sl, ids, ctx := twoRouters(t)
	m := confirmCode.FindStringSubmatch(say(t, wa, ctx, "+alice", "/link slack:UALICE"))
	require.Len(t, m, 2)
	wrong := "000000"
	if m[1] == wrong {
		wrong = "111111"
	}
	assert.Contains(t, say(t, sl, ctx, "UALICE", "/confirm "+wrong), "no pending link")
	assert.Contains(t, say(t, sl, ctx, "UALICE", "/confirm "+m[1]), "no pending link",
		"one wrong guess cancels the request")
	_, err := ids.Resolve(ctx, "slack", "UALICE")
	assert.Error(t, err)
}

func TestLink_UnknownTransportRefused(t *testing.T) {
	wa, _, _, ctx := twoRouters(t)
	assert.Contains(t, say(t, wa, ctx, "+alice", "/link nosuch:x"), "unknown transport")
	assert.Contains(t, say(t, wa, ctx, "+alice", "/unlink nosuch:x"), "unknown transport")
}
