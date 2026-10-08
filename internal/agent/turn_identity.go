package agent

import (
	"context"
	"sync"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// turnIdentities lends a running turn's verified identity to the tool
// calls a provider subprocess makes on that session's behalf. The
// claude CLI runs its own tools and asks the toolgate socket about
// them on the daemon's context, which carries no identity; without
// this map RBAC, OPA and multi-party approval would judge those calls
// as anonymous. Entries live only while a turn for the session runs.
//
// When two turns for one session overlap, the most recent identity is
// lent until both end (sessions are per-conversation, so overlap is a
// retry, not a second user).
type turnIdentities struct {
	mu sync.Mutex
	m  map[string]*lentIdentity
}

type lentIdentity struct {
	id   sso.Identity
	refs int
}

// enter lends id for sessionID and returns the func that ends the
// loan. An empty sessionID lends nothing.
func (t *turnIdentities) enter(sessionID string, id sso.Identity) (leave func()) {
	if sessionID == "" {
		return func() {}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*lentIdentity{}
	}
	e := t.m[sessionID]
	if e == nil {
		e = &lentIdentity{}
		t.m[sessionID] = e
	}
	e.id = id
	e.refs++
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if e.refs--; e.refs == 0 {
			delete(t.m, sessionID)
		}
	}
}

// lookup returns the identity lent for sessionID, if a turn is running.
func (t *turnIdentities) lookup(sessionID string) (sso.Identity, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[sessionID]
	if !ok {
		return sso.Identity{}, false
	}
	return e.id, true
}

// lendTurnIdentity lends ctx's verified identity to sessionID for the
// duration of a turn; the returned func ends the loan.
func (a *Agent) lendTurnIdentity(ctx context.Context, sessionID string) func() {
	id, ok := sso.IdentityFromContext(ctx)
	if !ok {
		return func() {}
	}
	return a.turnIDs.enter(sessionID, id)
}

// withTurnIdentity attaches the identity of the turn running for
// sessionID to ctx, unless ctx already carries one.
func (a *Agent) withTurnIdentity(ctx context.Context, sessionID string) context.Context {
	if _, ok := sso.IdentityFromContext(ctx); ok {
		return ctx
	}
	if id, ok := a.turnIDs.lookup(sessionID); ok {
		return sso.WithIdentity(ctx, id)
	}
	return ctx
}
