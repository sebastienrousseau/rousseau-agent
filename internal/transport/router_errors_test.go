package transport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

// ---- fakes -----------------------------------------------------------

// fakeResolver is an in-memory identity.Resolver with a failure knob
// per method, so each error branch of the chat commands can be driven
// deterministically.
type fakeResolver struct {
	resolveID  identity.ID
	resolveErr error

	provisionID  identity.ID
	provisionErr error

	linkErr   error
	unlinkErr error

	record identity.Identity
	getErr error

	linked   []string
	unlinked []string
}

func (f *fakeResolver) Resolve(context.Context, string, string) (identity.ID, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.resolveID, nil
}

func (f *fakeResolver) Provision(context.Context, string, string, string) (identity.ID, error) {
	if f.provisionErr != nil {
		return "", f.provisionErr
	}
	return f.provisionID, nil
}

func (f *fakeResolver) Link(_ context.Context, _ identity.ID, tp, sender string) error {
	if f.linkErr != nil {
		return f.linkErr
	}
	f.linked = append(f.linked, tp+":"+sender)
	return nil
}

func (f *fakeResolver) Unlink(_ context.Context, tp, sender string) error {
	if f.unlinkErr != nil {
		return f.unlinkErr
	}
	f.unlinked = append(f.unlinked, tp+":"+sender)
	return nil
}

func (f *fakeResolver) Get(context.Context, identity.ID) (identity.Identity, error) {
	if f.getErr != nil {
		return identity.Identity{}, f.getErr
	}
	return f.record, nil
}

func (f *fakeResolver) HandlesFor(context.Context, identity.ID) ([]identity.Handle, error) {
	return f.record.Handles, nil
}

// putErrJID fails every Put, exercising the mapping-persistence
// failure path in sessionFor.
type putErrJID struct {
	*memJID
	putErr error
}

func (j *putErrJID) Put(ctx context.Context, jid, id string) error {
	if j.putErr != nil {
		return j.putErr
	}
	return j.memJID.Put(ctx, jid, id)
}

func routerWith(t *testing.T, store SessionStore, jm JIDMapper, res identity.Resolver, logger *slog.Logger) *Router {
	t.Helper()
	return NewRouter(
		&stubRunner{reply: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "ok"}},
		}},
		store, jm, logger,
		RouterOptions{Identity: res, Transport: "whatsapp"},
	)
}

func capturingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ---- constructor -----------------------------------------------------

func TestNewRouter_NilLoggerFallsBackToDefault(t *testing.T) {
	r := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), nil, RouterOptions{})
	require.NotNil(t, r)
	assert.NotNil(t, r.logger)
}

// ---- session persistence --------------------------------------------

// TestRouter_PostTurnSaveFailureStillReplies: losing the transcript
// write must not swallow the answer the user is waiting for.
func TestRouter_PostTurnSaveFailureStillReplies(t *testing.T) {
	store := newMemStore()
	jm := newMemJID()
	sess := agent.NewSession("chat: +123")
	store.sessions[sess.ID] = sess
	jm.data["+123"] = sess.ID
	store.saveErr = errors.New("disk full")

	var logs bytes.Buffer
	r := routerWith(t, store, jm, nil, capturingLogger(&logs))

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Contains(t, logs.String(), "router.save_failed")
}

func TestRouter_NewSessionSaveFailureSurfaces(t *testing.T) {
	store := newMemStore()
	store.saveErr = errors.New("disk full")
	r := routerWith(t, store, newMemJID(), nil, silentLogger())

	_, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "hello"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router: session")
	assert.ErrorContains(t, err, "disk full")
}

func TestRouter_JIDMappingWriteFailureSurfaces(t *testing.T) {
	jm := &putErrJID{memJID: newMemJID(), putErr: errors.New("mapping table locked")}
	r := routerWith(t, newMemStore(), jm, nil, silentLogger())

	_, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "hello"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "mapping table locked")
}

// ---- identity command error paths -----------------------------------

func TestRouter_UnlinkUsageMessageOnBadInput(t *testing.T) {
	res := &fakeResolver{resolveID: "id-1"}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	for _, body := range []string{"/unlink", "/unlink no-colon", "/unlink a b"} {
		got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: body})
		require.NoError(t, err)
		assert.Equal(t, "usage: /unlink <transport>:<sender>", got, "body %q", body)
	}
	assert.Empty(t, res.unlinked, "a malformed command must not touch the identity store")
}

func TestRouter_WhoamiReportsProvisionFailure(t *testing.T) {
	res := &fakeResolver{
		resolveErr:   identity.ErrNotLinked,
		provisionErr: errors.New("identity store offline"),
	}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Contains(t, got, "whoami: identity store offline")
}

func TestRouter_WhoamiReportsLookupFailure(t *testing.T) {
	res := &fakeResolver{resolveID: "id-1", getErr: identity.ErrIdentityNotFound}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Contains(t, got, "whoami: identity: not found")
}

// TestRouter_WhoamiRendersRecord pins the human-readable shape of the
// reply, including the optional display line.
func TestRouter_WhoamiRendersRecord(t *testing.T) {
	res := &fakeResolver{
		resolveID: "id-1",
		record: identity.Identity{
			ID:             "id-1",
			PrimaryDisplay: "Alice",
			Handles: []identity.Handle{
				{Transport: "whatsapp", Sender: "+123"},
				{Transport: "slack", Sender: "U01"},
			},
		},
	}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Contains(t, got, "identity: id-1")
	assert.Contains(t, got, "display:  Alice")
	assert.Contains(t, got, "handles:  2")
	assert.Contains(t, got, "  whatsapp:+123")
	assert.Contains(t, got, "  slack:U01")
}

func TestRouter_LinkReportsProvisionFailure(t *testing.T) {
	res := &fakeResolver{
		resolveErr:   identity.ErrNotLinked,
		provisionErr: errors.New("identity store offline"),
	}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/link slack:U01"})
	require.NoError(t, err)
	assert.Contains(t, got, "link: identity store offline")
	assert.Empty(t, res.linked)
}

func TestRouter_LinkReportsAlreadyLinked(t *testing.T) {
	res := &fakeResolver{resolveID: "id-1", linkErr: identity.ErrAlreadyLinked}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/link slack:U01"})
	require.NoError(t, err)
	assert.Contains(t, got, "link: identity: handle already linked")
}

func TestRouter_UnlinkReportsFailure(t *testing.T) {
	res := &fakeResolver{resolveID: "id-1", unlinkErr: errors.New("no such handle")}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/unlink slack:U01"})
	require.NoError(t, err)
	assert.Contains(t, got, "unlink: no such handle")
}

// TestRouter_ResolveTransportErrorIsNotSwallowed: only ErrNotLinked
// triggers auto-provisioning; any other resolver failure must be
// reported rather than silently creating a duplicate identity.
func TestRouter_ResolveTransportErrorIsNotSwallowed(t *testing.T) {
	res := &fakeResolver{
		resolveErr:  errors.New("database is locked"),
		provisionID: "should-not-be-used",
	}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	got, err := r.Handle(context.Background(), IncomingMessage{From: "+123", Body: "/whoami"})
	require.NoError(t, err)
	assert.Contains(t, got, "whoami: database is locked")
}

func TestResolveOrProvision_ProvisionsOnFirstSight(t *testing.T) {
	res := &fakeResolver{resolveErr: identity.ErrNotLinked, provisionID: "fresh-id"}
	r := routerWith(t, newMemStore(), newMemJID(), res, silentLogger())

	id, err := r.resolveOrProvision(context.Background(), "+123", "+123")
	require.NoError(t, err)
	assert.Equal(t, identity.ID("fresh-id"), id)
}
