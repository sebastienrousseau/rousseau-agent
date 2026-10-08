package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// TestA2AHandler_AuthedSenderIgnoresFromAgent: with auth on, the
// session sender comes from the authenticated peer only; a body-chosen
// from_agent never stands in for it.
func TestA2AHandler_AuthedSenderIgnoresFromAgent(t *testing.T) {
	t.Parallel()
	h := newA2ATaskHandler(nil, nil, discardLogger())
	h.requirePeer = true

	sess := h.newSession(a2a.Task{FromAgent: "victim", Peer: "tok:abc", Prompt: "p"})
	assert.Equal(t, "a2a/tok:abc", sess.Sender)

	err := h.validateTask(a2a.Task{FromAgent: "victim", Prompt: "p"})
	require.Error(t, err, "a task without an authenticated peer must be refused when auth is on")
	sess = h.newSession(a2a.Task{FromAgent: "victim", Prompt: "p"})
	assert.NotEqual(t, "a2a/victim", sess.Sender, "from_agent must never become the sender when auth is on")
}
