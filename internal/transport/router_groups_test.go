package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// historyRunner replies with every user text the session holds, so a
// test can see exactly which history a turn was given.
type historyRunner struct{}

func (historyRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	var seen []string
	for _, m := range sess.Messages {
		if m.Role != agent.RoleUser {
			continue
		}
		for _, c := range m.Content {
			seen = append(seen, c.Text)
		}
	}
	reply := agent.NewAssistantText(strings.Join(seen, "|"))
	sess.Append(reply)
	return reply, nil
}

func groupMsg(from, conv, body string) IncomingMessage {
	return IncomingMessage{From: from, Conversation: conv, Body: body, At: time.Now()}
}

func dmMsg(from, body string) IncomingMessage {
	return IncomingMessage{From: from, Conversation: from, IsDirect: true, Body: body, At: time.Now()}
}

func TestRouter_GroupMessageDroppedByDefault(t *testing.T) {
	store := newMemStore()
	r := NewRouter(historyRunner{}, store, newMemJID(), silentLogger(), RouterOptions{
		Transport: "telegram", Allowlist: []string{"alice"},
	})
	reply, err := r.Handle(context.Background(), groupMsg("alice", "-100group", "hello group"))
	require.NoError(t, err)
	assert.Empty(t, reply, "a group message is ignored unless allow_groups is set")
	sessions, err := store.ListBySender(context.Background(), "telegram:alice", 10)
	require.NoError(t, err)
	assert.Empty(t, sessions, "a dropped group message creates no session")
}

func TestRouter_GroupSessionSeparateFromDM(t *testing.T) {
	r := NewRouter(historyRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "telegram", Allowlist: []string{"alice"}, AllowGroups: true,
	})
	ctx := context.Background()
	_, err := r.Handle(ctx, dmMsg("alice", "my private note"))
	require.NoError(t, err)

	reply, err := r.Handle(ctx, groupMsg("alice", "-100group", "hello group"))
	require.NoError(t, err)
	assert.Equal(t, "hello group", reply, "the group turn sees only the group's history")
	assert.NotContains(t, reply, "my private note")

	reply, err = r.Handle(ctx, dmMsg("alice", "again"))
	require.NoError(t, err)
	assert.Equal(t, "my private note|again", reply, "the DM keeps its own history")
}

func TestRouter_DirectWithoutFlagStillHandled(t *testing.T) {
	// Callers that leave Conversation empty, or set it to the sender,
	// are one-to-one by construction.
	r := NewRouter(historyRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{Transport: "x"})
	for _, msg := range []IncomingMessage{
		{From: "bob", Body: "one"},
		{From: "bob", Conversation: "bob", Body: "two"},
	} {
		reply, err := r.Handle(context.Background(), msg)
		require.NoError(t, err)
		assert.NotEmpty(t, reply)
	}
}

func TestConversationKey(t *testing.T) {
	assert.Equal(t, "bob", ConversationKey(IncomingMessage{From: "bob"}))
	assert.Equal(t, "bob", ConversationKey(dmMsg("bob", "")))
	assert.Equal(t, "bob", ConversationKey(IncomingMessage{From: "bob", Conversation: "!room", IsDirect: true}))
	assert.Equal(t, "bob#!room", ConversationKey(IncomingMessage{From: "bob", Conversation: "!room"}))
	assert.True(t, IsGroup(IncomingMessage{From: "bob", Conversation: "!room"}))
	assert.False(t, IsGroup(IncomingMessage{From: "bob", Conversation: "!room", IsDirect: true}))
}

// A group message must not be steered into the sender's running DM
// turn: the supervisor keys turns by conversation.
func TestSupervisor_GroupDoesNotSteerDMTurn(t *testing.T) {
	assert.NotEqual(t, ConversationKey(dmMsg("alice", "")), ConversationKey(groupMsg("alice", "g", "")))
}

func TestDropGroups(t *testing.T) {
	calls := 0
	h := DropGroups(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		calls++
		return "nothing running", nil
	}), silentLogger())
	reply, err := h.Handle(context.Background(), groupMsg("alice", "g", "/status"))
	require.NoError(t, err)
	assert.Empty(t, reply, "control verbs in a group get no answer either")
	assert.Zero(t, calls)
	reply, err = h.Handle(context.Background(), dmMsg("alice", "/status"))
	require.NoError(t, err)
	assert.Equal(t, "nothing running", reply)
}
