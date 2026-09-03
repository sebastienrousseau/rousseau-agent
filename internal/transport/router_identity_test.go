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
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
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

func (a *storeAdapter) ListBySender(ctx context.Context, sender string, limit int) ([]state.Summary, error) {
	return a.s.ListBySender(ctx, sender, limit)
}

func (a *storeAdapter) Delete(ctx context.Context, id string) error {
	return a.s.Delete(ctx, id)
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

func TestRouter_ClearStartsFreshSession(t *testing.T) {
	// /clear must produce a new session id via jidMap (upsert
	// semantics) so the next inbound builds LLM context from
	// scratch. Old session stays in the DB — pinned separately
	// below to catch a "clear = delete" refactor regression.
	r, _, ctx := setup(t)

	// Send an initial message so a session gets provisioned.
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+555", Body: "hello"})
	require.NoError(t, err)

	// Reach into the test's shared store adapter for the
	// jidMap so we can capture the "before" session id.
	//
	// The test's setup uses sqlitestore.NewJIDMap directly, so
	// this is just verifying end-to-end: /clear replaces the
	// mapping and the reply text is the operator-facing one.
	reply, err := r.Handle(ctx, transport.IncomingMessage{From: "+555", Body: "/clear"})
	require.NoError(t, err)
	assert.Contains(t, reply, "cleared")
	assert.Contains(t, reply, "fresh session")
}

func TestRouter_ClearOnFreshSenderIsIdempotent(t *testing.T) {
	// A sender who has never messaged before typing /clear must
	// still get a friendly success reply — the router provisions
	// a fresh session for them without erroring on the missing
	// prior mapping. Prevents a UX regression where operators
	// running `--allow <jid>` on a new deploy hit an error on
	// their very first probe.
	r, _, ctx := setup(t)
	reply, err := r.Handle(ctx, transport.IncomingMessage{From: "+never-here", Body: "/clear"})
	require.NoError(t, err)
	assert.Contains(t, reply, "cleared")
}

func TestRouter_ClearIsScopedToSender(t *testing.T) {
	// The critical safety invariant: /clear from sender A must
	// NOT touch sender B's session. jidMap.Put upserts on the
	// (from) key, so a single-key write cannot spill across
	// senders — but the invariant is important enough to pin
	// explicitly so a future "helpful" refactor that batches
	// writes or uses a broader key would fail this test.
	//
	// Real-world concern: an operator's WhatsApp bot may be
	// paired via multi-device to their personal account. Even
	// though the router only handles messages sent to the
	// allowlisted destination, the DB shape needs to guarantee
	// per-sender isolation.
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)

	r := transport.NewRouter(
		&staticRunner{reply: "ok"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{},
	)

	// Sender A gets a session assigned via a first message.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)
	// Sender B likewise.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+bob", Body: "hi"})
	require.NoError(t, err)

	bobBefore, ok, err := jm.Get(ctx, "+bob")
	require.NoError(t, err)
	require.True(t, ok, "bob must have a session assigned before the /clear probe")

	// Sender A clears. This must NOT touch bob's mapping.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/clear"})
	require.NoError(t, err)

	bobAfter, ok, err := jm.Get(ctx, "+bob")
	require.NoError(t, err)
	require.True(t, ok, "bob's session must still exist after alice's /clear")
	assert.Equal(t, bobBefore, bobAfter, "alice's /clear MUST NOT rewrite bob's session mapping")
}

func TestRouter_ClearWorksWithoutIdentityResolver(t *testing.T) {
	// Same regression class as TestRouter_VersionWorksWithoutIdentityResolver:
	// /clear must run even in the daemon's default no-identity
	// wiring (production has always been like this for
	// single-transport deployments). The handler needs only the
	// session store + jidMap, both always available.
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	jm, err := sqlitestore.NewJIDMap(ctx, store)
	require.NoError(t, err)

	r := transport.NewRouter(
		&staticRunner{reply: "SHOULD NOT REACH LLM"},
		&storeAdapter{s: store},
		jm,
		silent(),
		transport.RouterOptions{}, // no Identity
	)
	reply, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/clear"})
	require.NoError(t, err)
	assert.Contains(t, reply, "cleared", "/clear must answer even without an Identity resolver")
}

// -- session lifecycle verbs -----------------------------------------------

func TestRouter_SessionsEmptyGuidesFirstUse(t *testing.T) {
	// A never-messaged sender running /sessions must see a
	// friendly onboarding message, not a bare "(none)" that
	// leaves them wondering if the command worked.
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+555", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, got, "no saved sessions yet")
	assert.Contains(t, got, "/name")
}

func TestRouter_SessionsListsSenderOwnedOnly(t *testing.T) {
	// After a first message provisions a session, /sessions
	// lists it and marks it current. Sender B's session must NOT
	// appear in sender A's listing — the scope-isolation invariant.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+bob", Body: "hi"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, got, "sessions (newest first")
	assert.Contains(t, got, "chat: +alice")
	assert.Contains(t, got, "*") // current marker
	assert.NotContains(t, got, "chat: +bob", "alice's /sessions MUST NOT include bob's session")
}

// TestRouter_SessionsShowsLastUserPreview pins the /sessions
// preview behaviour: each listing row should include a "↳ …"
// snippet of the last user message on that session so
// operators can distinguish sessions without /resume-ing into
// each one. Multi-turn sessions should show the FRESHEST
// user turn, not the opener.
func TestRouter_SessionsShowsLastUserPreview(t *testing.T) {
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "opening question about deploys"})
	require.NoError(t, err)
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "followup on staging cluster"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, got, "↳", "preview marker must appear on the listing row")
	assert.Contains(t, got, "followup on staging cluster", "preview must be the LATEST user message")
	assert.NotContains(t, got, "opening question about deploys", "preview must NOT be the opener when a later user turn exists")
}

// TestRouter_SessionsPreviewTruncatesLongInput pins the
// truncation contract: previews longer than the char cap get
// clipped with an ellipsis so the listing stays readable in a
// WhatsApp bubble.
func TestRouter_SessionsPreviewTruncatesLongInput(t *testing.T) {
	r, _, ctx := setup(t)
	long := strings.Repeat("word ", 40) // ~200 chars
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: long})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, got, "↳", "preview marker must appear")
	assert.Contains(t, got, "…", "long input must truncate with an ellipsis")
}

func TestRouter_NameRenamesCurrent(t *testing.T) {
	// /name updates the current session's Title. Verify the rename
	// surfaces on the next /sessions listing.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: `/name "deploy planning"`})
	require.NoError(t, err)
	assert.Contains(t, got, "deploy planning")

	sessions, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, sessions, "deploy planning")
}

func TestRouter_NameEmptyShowsCurrentName(t *testing.T) {
	// /name with no args used to just print usage. That's not
	// discoverable — operators expect "show me the current
	// value" from a no-arg query. Now it shows the current
	// session's title AND the usage line so both use cases
	// work.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hi"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/name"})
	require.NoError(t, err)
	assert.Contains(t, got, "current session name")
	assert.Contains(t, got, `"chat: +alice"`, "current title must be echoed")
	assert.Contains(t, strings.ToLower(got), "usage:")
}

func TestRouter_ResumeSwitchesActiveSession(t *testing.T) {
	// /clear creates session-2 while session-1 stays in the DB.
	// /sessions shows both; /resume <shortid-of-session-1>
	// switches back so the next inbound continues session-1.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "first message"})
	require.NoError(t, err)
	// Grab session-1's id via /sessions before /clear.
	before, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	// Extract the short-id from the listing (last field on the
	// first row after the header + blank).
	shortID := lastToken(firstMatchingLine(before, "chat: +alice"))
	require.Len(t, shortID, 8)

	// Clear → session-2 becomes current.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/clear"})
	require.NoError(t, err)

	// Resume by short-id → session-1 becomes current again.
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/resume " + shortID})
	require.NoError(t, err)
	assert.Contains(t, got, "resumed session")
}

func TestRouter_ResumeUnknownShortidReplies(t *testing.T) {
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/resume deadbeef"})
	require.NoError(t, err)
	assert.Contains(t, got, "no session found")
}

func TestRouter_NameStripsSmartQuotes(t *testing.T) {
	// iOS autocorrect converts ASCII " into curly “ ”. Before
	// the trimQuotes widening, an iPhone user typing
	// /name "planning" landed with title = `“planning”` (curly
	// quotes as literal chars). Pin the fix: all four quote
	// shapes (straight double / curly double / straight single
	// / curly single) must strip cleanly.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hi"})
	require.NoError(t, err)

	// Smart double quotes (what iOS autocorrect actually sends).
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/name “deploy plan”"})
	require.NoError(t, err)
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, listing, "deploy plan", "curly-quoted name must strip to plain text")
	assert.NotContains(t, listing, "“deploy", "curly quotes must NOT survive")
}

func TestRouter_SaveSnapshotsCurrent(t *testing.T) {
	// /save "name" forks the current session into a named
	// snapshot the user can /resume later. Verifies:
	// - snapshot appears in /sessions
	// - current session is NOT switched (jidMap still points at
	//   the original) — user continues where they were
	// - snapshot has its own short-id distinct from the current
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: `/save "checkpoint-1"`})
	require.NoError(t, err)
	assert.Contains(t, got, "saved snapshot")
	assert.Contains(t, got, "checkpoint-1")
	assert.Contains(t, got, "current session")

	// /sessions shows both the current AND the snapshot.
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, listing, "chat: +alice", "current session must still be listed")
	assert.Contains(t, listing, "checkpoint-1", "snapshot must be listed")
}

func TestRouter_SaveEmptyAutoNamesWithTimestamp(t *testing.T) {
	// UX evolution: /save alone used to return the usage line,
	// but operators reasonably expect "just save it now" to
	// mean "just save it now." Default to a timestamped
	// snapshot name so the verb always does something useful.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hi"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/save"})
	require.NoError(t, err)
	assert.Contains(t, got, "saved snapshot")
	assert.Contains(t, got, "snapshot 20", "auto-name should include a timestamp like 'snapshot 2026-…'")

	// The snapshot should appear in /sessions with a
	// timestamp-shaped title.
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	assert.Contains(t, listing, "snapshot 20", "auto-named snapshot must appear in /sessions")
}

func TestRouter_SaveDoesNotSwitchCurrentSession(t *testing.T) {
	// Critical invariant: /save is a FORK, not a rename or
	// switch. After /save the user's next message must land
	// on the ORIGINAL session, not the snapshot. Pinned so a
	// well-meaning refactor that treats /save like /clear
	// would fail this test.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "first"})
	require.NoError(t, err)

	// Grab the pre-save session id via /sessions.
	before, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	originalShortID := lastToken(firstMatchingLine(before, "chat: +alice"))
	require.Len(t, originalShortID, 8)

	// /save creates the snapshot.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: `/save "snap"`})
	require.NoError(t, err)

	// After /save, /sessions must show the original marked as current.
	after, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	currentLine := firstMatchingLine(after, "chat: +alice")
	assert.Contains(t, currentLine, "*", "original session must still be marked current")
	assert.Contains(t, currentLine, originalShortID, "original session must still be at the same short-id")
	snapLine := firstMatchingLine(after, "snap")
	assert.NotContains(t, snapLine, "*", "snapshot must NOT be the current session")
}

func TestRouter_SaveIsAtomicSnapshot(t *testing.T) {
	// A snapshot must contain a frozen copy of the messages —
	// future appends to the live session must NOT mutate the
	// snapshot. Pinned so a shallow-copy bug would fail here.
	// (staticRunner doesn't append its reply, so each inbound
	// adds exactly one message to the session.)
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "first"})
	require.NoError(t, err)

	// Snapshot at msg-count = 1 (user "first").
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: `/save "snap"`})
	require.NoError(t, err)

	// Continue the conversation in the live session.
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "second"})
	require.NoError(t, err)
	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "third"})
	require.NoError(t, err)

	// The snapshot listing must still show the snap-time count
	// even though the current session has grown to 3.
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	snapLine := firstMatchingLine(listing, "snap")
	assert.Contains(t, snapLine, "(1 msg)", "snapshot must be frozen at snap-time msg count")
	currentLine := firstMatchingLine(listing, "chat: +alice")
	assert.Contains(t, currentLine, "(3 msg)", "current session must have grown past snapshot")
}

func TestRouter_DeleteRemovesNonCurrentSession(t *testing.T) {
	// /delete requires a shortid AND refuses to delete the
	// current session. Sequence: provision → clear (creates 2nd
	// session) → find shortid of the OLD session (first-created)
	// → /delete it.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "first"})
	require.NoError(t, err)
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	oldShortID := lastToken(firstMatchingLine(listing, "chat: +alice"))
	require.Len(t, oldShortID, 8)

	_, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/clear"})
	require.NoError(t, err)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/delete " + oldShortID})
	require.NoError(t, err)
	assert.Contains(t, got, "deleted session")
}

func TestRouter_DeleteRefusesCurrent(t *testing.T) {
	// Deleting the currently-active session would leave the user
	// pointing at a non-existent id. Refuse and nudge to /clear.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hello"})
	require.NoError(t, err)
	listing, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/sessions"})
	require.NoError(t, err)
	currentShortID := lastToken(firstMatchingLine(listing, "chat: +alice"))
	require.Len(t, currentShortID, 8)

	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/delete " + currentShortID})
	require.NoError(t, err)
	assert.Contains(t, got, "refusing to delete the current session")
	assert.Contains(t, got, "/clear")
}

func TestRouter_HelpEnumeratesAllVerbs(t *testing.T) {
	// /help is the operator's discovery surface — every
	// synchronous verb must appear in the listing (both full
	// form and shortcut) so a new user can learn the CLI
	// without reading source. Pinned so a new verb added to
	// syncCommands without a /help entry fails this test.
	r, _, ctx := setup(t)
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/help"})
	require.NoError(t, err)
	for _, must := range []string{
		"/sessions", "/ls",
		"/clear", "/c",
		"/name", "/n",
		"/save", "/s",
		"/resume", "/r",
		"/delete", "/d",
		"/status", "/st",
		"/pause", "/p",
		"/cancel", "/x",
		"/whoami", "/w",
		"/link", "/lk",
		"/unlink", "/ul",
		"/login", "/li",
		"/logout", "/lo",
		"/approve", "/ap",
		"/deny", "/ny",
		"/version", "/v",
		"/help", "/h",
	} {
		assert.Contains(t, got, must, "help listing must mention %s", must)
	}
}

func TestRouter_HelpShortcutAlsoWorks(t *testing.T) {
	// /h must produce identical output to /help.
	r, _, ctx := setup(t)
	long, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/help"})
	require.NoError(t, err)
	short, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/h"})
	require.NoError(t, err)
	assert.Equal(t, long, short)
}

func TestRouter_VersionShortcutAlsoWorks(t *testing.T) {
	// /v must return the same build stamp as /version.
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
		transport.RouterOptions{BuildStamp: stamp},
	)
	long, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/version"})
	require.NoError(t, err)
	short, err := r.Handle(ctx, transport.IncomingMessage{From: "+123", Body: "/v"})
	require.NoError(t, err)
	assert.Equal(t, long, short)
}

func TestRouter_ShortcutsCanonicalise(t *testing.T) {
	// /c, /s, /n, /r, /d, /ls must behave identically to their
	// canonical forms. Pinning both the syncCommands membership
	// AND the alias-table dispatch so a future refactor that
	// touches one but not the other fails this test.
	r, _, ctx := setup(t)
	_, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "hi"})
	require.NoError(t, err)

	// /s should SAVE (Ctrl+S muscle memory — same as /save).
	got, err := r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/s"})
	require.NoError(t, err)
	assert.Contains(t, got, "saved snapshot")

	// /ls should list sessions (shell muscle memory).
	got, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/ls"})
	require.NoError(t, err)
	assert.Contains(t, got, "sessions (newest first")

	// /c should clear (same as /clear).
	got, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: "/c"})
	require.NoError(t, err)
	assert.Contains(t, got, "cleared")

	// /n "..." should rename (same as /name).
	got, err = r.Handle(ctx, transport.IncomingMessage{From: "+alice", Body: `/n "shortcut test"`})
	require.NoError(t, err)
	assert.Contains(t, got, "shortcut test")
}

// firstMatchingLine returns the first line of text that contains
// needle, or "" if none. Small local helper — keeps the tests
// tolerant of layout drift in the /sessions reply format.
func firstMatchingLine(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// lastToken returns the whitespace-delimited last token of s
// (used to pull the short-id off the end of a /sessions row).
func lastToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
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
