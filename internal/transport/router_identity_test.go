package transport_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// staticRunner is a TurnRunner that returns a fixed reply — enough
// for router tests, no real LLM needed.
type staticRunner struct{ reply string }

func (r *staticRunner) Turn(_ context.Context, s *agent.Session) (agent.Message, error) {
	return agent.Message{
		Role:    agent.RoleAssistant,
		Content: []agent.Content{{Kind: agent.ContentText, Text: r.reply}},
	}, nil
}

// storeAdapter satisfies transport.SessionStore over the SQLite Store.
type storeAdapter struct{ s *sqlitestore.Store }

func (a *storeAdapter) Save(ctx context.Context, sess *agent.Session) error {
	return a.s.Save(ctx, sess)
}
func (a *storeAdapter) Load(ctx context.Context, id string) (*agent.Session, error) {
	return a.s.Load(ctx, id)
}

func silent() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// setup returns a fully-wired router with an identity resolver so
// tests can drive both the LLM path and the chat commands.
func setup(t *testing.T) (*transport.Router, *sqlitestore.IdentityStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)

	ids, err := sqlitestore.NewIdentityStore(ctx, store)
	require.NoError(t, err)

	r := transport.NewRouter(
		&staticRunner{reply: "ok"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{
			Identity:  ids,
			Transport: "whatsapp",
			// Deliberately no allowlist so chat-command tests aren't blocked.
		},
	)
	return r, ids, ctx
}

func TestRouter_WhoamiAutoProvisions(t *testing.T) {
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Contains(t, got, "identity:")
	assert.Contains(t, got, "whatsapp:+123")
	assert.Contains(t, got, "handles:  1")
}

func TestRouter_LinkAdditionalHandle(t *testing.T) {
	r, ids, ctx := setup(t)
	// Provision +123 via first /whoami.
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)

	// Now link a slack handle to the same identity.
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/link slack:U01234"})
	require.NoError(t, err)
	assert.Contains(t, got, "linked slack:U01234 to identity")

	// The resolver now knows about both handles.
	id, err := ids.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	id2, err := ids.Resolve(ctx, "slack", "U01234")
	require.NoError(t, err)
	assert.Equal(t, id, id2)
}

func TestRouter_LinkUsageMessageOnBadInput(t *testing.T) {
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/link no-colon"})
	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(got), "usage:")
}

func TestRouter_UnlinkRemovesHandle(t *testing.T) {
	r, ids, ctx := setup(t)
	// Provision + link.
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/link slack:U01234"})
	require.NoError(t, err)

	// Unlink the slack handle.
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/unlink slack:U01234"})
	require.NoError(t, err)
	assert.Contains(t, got, "unlinked slack:U01234")

	// Resolver no longer knows about slack:U01234.
	_, err = ids.Resolve(ctx, "slack", "U01234")
	assert.Error(t, err)
}

func TestRouter_UnknownSlashDoesNotShortCircuit(t *testing.T) {
	// A message starting with "/" that isn't a known command must
	// fall through to the LLM (the staticRunner returns "ok").
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/wat"})
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
}

func TestRouter_RegularMessageBypassesCommandInterception(t *testing.T) {
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
}

func TestRouter_VersionEchoesBuildStamp(t *testing.T) {
	// /version is the operator-facing "which binary is answering"
	// probe. Must echo the exact BuildStamp the daemon passed in,
	// prefixed with "rousseau " so the reply is self-identifying
	// even if forwarded / screenshotted out of context.
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)
	ids, err := sqlitestore.NewIdentityStore(ctx, store)
	require.NoError(t, err)

	stamp := "v0.0.3-99-gdeadbee (commit deadbee, built 2026-01-01T00:00:00Z)"
	r := transport.NewRouter(
		&staticRunner{reply: "ok"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{
			Identity:   ids,
			Transport:  "whatsapp",
			BuildStamp: stamp,
		},
	)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/version"})
	require.NoError(t, err)
	assert.Equal(t, "rousseau "+stamp, got)
}

func TestRouter_VersionFallbackWhenBuildStampEmpty(t *testing.T) {
	// The daemon SHOULD always inject a build stamp, but dev
	// builds without ldflags exist. /version must answer rather
	// than error or fall through to the LLM.
	r, _, ctx := setup(t) // setup passes no BuildStamp
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/version"})
	require.NoError(t, err)
	assert.Contains(t, got, "unknown build")
}

func TestRouter_VersionWorksWithoutIdentityResolver(t *testing.T) {
	// Regression pin: the daemon does NOT wire an Identity resolver
	// today (see cli/daemon.go — RouterOptions{} has no Identity
	// field). Before this fix, /version was buried inside
	// handleIdentityCommand which is gated on `r.identity != nil`,
	// so /version fell through to the LLM and returned fabricated
	// text like "Unknown command." /version has no identity
	// dependency; it must answer regardless of whether identity is
	// wired.
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)

	stamp := "v0.0.3-99-gdeadbee (commit deadbee, built 2026-01-01T00:00:00Z)"
	r := transport.NewRouter(
		&staticRunner{reply: "SHOULD NOT REACH LLM"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{
			BuildStamp: stamp,
			// Deliberately no Identity — mirrors production.
		},
	)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/version"})
	require.NoError(t, err)
	assert.Equal(t, "rousseau "+stamp, got, "/version must answer even without an Identity resolver")
}

func TestRouter_NilIdentityDisablesCommandInterception(t *testing.T) {
	// A router without an Identity resolver treats /whoami as
	// regular text (no interception) — backwards-compat guarantee.
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)

	r := transport.NewRouter(
		&staticRunner{reply: "handled by LLM"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{}, // no Identity, no Transport
	)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Equal(t, "handled by LLM", got, "with no identity, /whoami must reach the LLM")
}
