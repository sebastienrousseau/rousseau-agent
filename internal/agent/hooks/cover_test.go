package hooks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

// TestNew_NilLoggerAndEmptyEventListsAreDropped covers the constructor's
// normalisation: a nil logger must not panic, and an event registered
// with an empty slice must behave exactly like an unregistered one.
func TestNew_NilLoggerAndEmptyEventLists(t *testing.T) {
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse:  nil,
		hooks.EventPostToolUse: {},
	}, nil)
	require.NotNil(t, s)

	for _, event := range []hooks.Event{hooks.EventPreToolUse, hooks.EventPostToolUse} {
		v, err := s.Run(context.Background(), event, []byte(`{}`))
		require.NoError(t, err)
		assert.Equal(t, hooks.DecisionAllow, v.Decision)
	}
}

// TestRun_VerdictWithoutDecisionDefaultsToAllow proves a hook that emits
// only a reason (no decision field) is not mistaken for a denial.
func TestRun_VerdictWithoutDecisionDefaultsToAllow(t *testing.T) {
	path := writeShellHook(t, `printf '{"reason":"just commentary"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "chatty", Command: path},
			{Name: "denier", Command: writeShellHook(t, `printf '{"decision":"deny","reason":"nope"}'`)},
		},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionDeny, v.Decision, "the chatty hook must not short-circuit the deny")
	assert.Equal(t, "nope", v.Reason)
}

// TestRun_BrokenHooksFailOpen covers the two runOne error paths that a
// misconfigured or misbehaving hook produces: no command at all, and
// stdout that is not a verdict. Neither may lock the daemon out.
func TestRun_BrokenHooksFailOpen(t *testing.T) {
	tests := []struct {
		name string
		cfg  hooks.Config
	}{
		{"empty command", hooks.Config{Name: "blank"}},
		{"unparseable stdout", hooks.Config{
			Name:    "garbage",
			Command: writeShellHook(t, `printf 'this is not json'`),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := hooks.New(map[hooks.Event][]hooks.Config{
				hooks.EventPreToolUse: {tc.cfg},
			}, silentLogger())
			v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
			require.NoError(t, err)
			assert.Equal(t, hooks.DecisionAllow, v.Decision)
		})
	}
}
