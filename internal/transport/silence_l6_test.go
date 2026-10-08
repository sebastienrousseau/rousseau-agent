package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

func countingHandler(calls *int) Handler {
	return HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		*calls++
		return "reply", nil
	})
}

func onlyAlice(_ context.Context, from string) bool { return from == "alice" }

func TestSilenceRejected(t *testing.T) {
	cases := []struct {
		name, from, body string
		sso, reaches     bool
	}{
		{"allowed sender", "alice", "/status", false, true},
		{"stranger control verb", "mallory", "/status", true, false},
		{"stranger chat", "mallory", "hello", true, false},
		{"stranger login with sso", "mallory", "/login tok", true, true},
		{"stranger login shortcut", "mallory", "/li tok", true, true},
		{"stranger login without sso", "mallory", "/login tok", false, false},
		{"stranger logout", "mallory", "/logout", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := SilenceRejected(countingHandler(&calls), onlyAlice, tc.sso, silentLogger())
			reply, err := h.Handle(context.Background(), IncomingMessage{From: tc.from, Body: tc.body})
			require.NoError(t, err)
			if tc.reaches {
				assert.Equal(t, 1, calls)
				assert.Equal(t, "reply", reply)
				return
			}
			assert.Zero(t, calls, "a rejected sender reaches nothing downstream")
			assert.Empty(t, reply, "a rejected sender gets no reply")
		})
	}
}

func TestLogout_WithoutBindingEmitsNoAudit(t *testing.T) {
	sink := &captureAuditSink{}
	r := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp", Allowlist: []string{"+alice"},
		SSO: stubDirectory{}, SSOStore: newMemBindings(), AuditSink: sink,
	})
	reply, err := r.Handle(context.Background(), IncomingMessage{From: "+alice", Body: "/logout"})
	require.NoError(t, err)
	assert.Equal(t, "not signed in", reply)
	assert.Empty(t, sink.snapshot(), "nothing happened, so nothing is audited")
}

var _ sso.BindingStore = (*memBindings)(nil)
