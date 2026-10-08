package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// ErrNothingToResume means the sender has no checkpointed turn to
// continue.
var ErrNothingToResume = errors.New("router: nothing to resume")

// Resume continues from's turn from its last checkpoint after a
// restart cut it off, and returns the reply to deliver. The session is
// well-formed at every checkpoint, so the model picks up after the
// last complete iteration with every earlier tool result in view. A
// session that already ends with the assistant's reply means the turn
// finished and only delivery was lost; that reply is returned as is.
func (r *Router) Resume(ctx context.Context, from string) (string, error) {
	sess, err := r.checkpointedSession(ctx, from)
	if err != nil {
		return "", err
	}
	if last := sess.Messages[len(sess.Messages)-1]; last.Role == agent.RoleAssistant {
		return firstText(last), nil
	}
	defer r.journalTurn(ctx, from, "(resumed after restart)")()
	final, err := r.runTurn(ctx, sess)
	r.saveSession(ctx, sess)
	if err != nil {
		return "", fmt.Errorf("router: resume: %w", err)
	}
	return firstText(final), nil
}

// checkpointedSession loads from's current session without creating
// one.
func (r *Router) checkpointedSession(ctx context.Context, from string) (*agent.Session, error) {
	id, ok, err := r.jidMap.Get(ctx, r.key(from))
	if err != nil {
		return nil, fmt.Errorf("router: resume: %w", err)
	}
	if !ok {
		return nil, ErrNothingToResume
	}
	sess, err := r.store.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("router: resume: %w", err)
	}
	if len(sess.Messages) == 0 {
		return nil, ErrNothingToResume
	}
	return sess, nil
}
