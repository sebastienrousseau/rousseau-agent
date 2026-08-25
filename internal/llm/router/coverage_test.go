package router

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// fakeProvider is an in-package agent.Provider double. It records the
// request it received so tests can assert the router forwarded the
// original payload untouched, and can be told to fail.
type fakeProvider struct {
	name string
	err  error
	got  []agent.Request
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return agent.Response{}, f.err
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: f.name}},
		},
		StopReason: agent.StopEndTurn,
		Model:      f.name,
	}, nil
}

func userMsg(text string) agent.Message {
	return agent.Message{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Kind: agent.ContentText, Text: text}},
	}
}

func toolUseMsg(n int) []agent.Message {
	out := make([]agent.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, agent.Message{
			Role: agent.RoleAssistant,
			Content: []agent.Content{{
				Kind:    agent.ContentToolUse,
				ToolUse: &agent.ToolUse{ID: "t", Name: "bash", Input: []byte(`{}`)},
			}},
		})
	}
	return out
}

func TestNew_RejectsEmptyProviders(t *testing.T) {
	_, err := New(Options{Default: "a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least the default")
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	r, err := New(Options{
		Default:   "a",
		Providers: map[string]agent.Provider{"a": &fakeProvider{name: "a"}},
	})
	require.NoError(t, err)
	assert.Same(t, slog.Default(), r.logger)
}

func TestNew_KeepsSuppliedLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(newDiscard(), nil))
	r, err := New(Options{
		Default:   "a",
		Providers: map[string]agent.Provider{"a": &fakeProvider{name: "a"}},
		Logger:    logger,
	})
	require.NoError(t, err)
	assert.Same(t, logger, r.logger)
}

// newDiscard returns a writer that swallows log output.
func newDiscard() *discardWriter { return &discardWriter{} }

type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestSelectChild_EveryRuleConstraint drives each individual constraint
// in ruleMatches through both its matching and its rejecting branch.
func TestSelectChild_EveryRuleConstraint(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}

	cases := []struct {
		name     string
		rule     Rule
		req      agent.Request
		wantKey  string
		wantRule string
	}{
		{
			name:     "message_len_min rejects a short message",
			rule:     Rule{MessageLenMin: 100, Use: "big"},
			req:      agent.Request{Messages: []agent.Message{userMsg("tiny")}},
			wantKey:  "fallback",
			wantRule: "default",
		},
		{
			name:     "message_len_min accepts a long message",
			rule:     Rule{MessageLenMin: 100, Use: "big"},
			req:      agent.Request{Messages: []agent.Message{userMsg(string(long))}},
			wantKey:  "big",
			wantRule: "big",
		},
		{
			name:     "tool_use_count_max rejects a tool-heavy session",
			rule:     Rule{ToolUseCountMax: 2, Use: "big"},
			req:      agent.Request{Messages: append(toolUseMsg(5), userMsg("go"))},
			wantKey:  "fallback",
			wantRule: "default",
		},
		{
			name:     "tool_use_count_max accepts a light session",
			rule:     Rule{Name: "light", ToolUseCountMax: 2, Use: "big"},
			req:      agent.Request{Messages: append(toolUseMsg(1), userMsg("go"))},
			wantKey:  "big",
			wantRule: "light",
		},
		{
			name:     "tool_use_count_min rejects a light session",
			rule:     Rule{ToolUseCountMin: 4, Use: "big"},
			req:      agent.Request{Messages: append(toolUseMsg(1), userMsg("go"))},
			wantKey:  "fallback",
			wantRule: "default",
		},
		{
			name:     "session_id_prefix rejects a shorter session id",
			rule:     Rule{SessionIDPrefix: "tenant-test-", Use: "big"},
			req:      agent.Request{SessionID: "ab"},
			wantKey:  "fallback",
			wantRule: "default",
		},
		{
			name:     "session_id_prefix rejects a same-length mismatch",
			rule:     Rule{SessionIDPrefix: "abc", Use: "big"},
			req:      agent.Request{SessionID: "abd"},
			wantKey:  "fallback",
			wantRule: "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(Options{
				Default: "fallback",
				Providers: map[string]agent.Provider{
					"fallback": &fakeProvider{name: "fallback"},
					"big":      &fakeProvider{name: "big"},
				},
				Rules: []Rule{tc.rule},
			})
			require.NoError(t, err)

			key, ruleName := r.selectChild(tc.req)
			assert.Equal(t, tc.wantKey, key)
			assert.Equal(t, tc.wantRule, ruleName)
		})
	}
}

// TestLastUserTextLen_SkipsNonUserTurns pins the "walk backwards past
// assistant turns" branch: only the trailing user turn counts.
func TestLastUserTextLen_SkipsNonUserTurns(t *testing.T) {
	msgs := []agent.Message{
		userMsg("1234567890"),
		{Role: agent.RoleAssistant, Content: []agent.Content{
			{Kind: agent.ContentText, Text: "a very long assistant reply that must not count"},
		}},
		{Role: agent.RoleSystem, Content: []agent.Content{
			{Kind: agent.ContentText, Text: "system noise"},
		}},
	}
	assert.Equal(t, 10, lastUserTextLen(msgs))
	assert.Equal(t, 0, lastUserTextLen(nil))
}

func TestLastUserTextLen_IgnoresNonTextBlocks(t *testing.T) {
	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "abc"},
			{Kind: agent.ContentImage, Image: &agent.Image{Data: make([]byte, 4096)}},
			{Kind: agent.ContentText, Text: "de"},
		},
	}}
	assert.Equal(t, 5, lastUserTextLen(msgs))
}

func TestHasPrefix_Table(t *testing.T) {
	cases := []struct {
		s, prefix string
		want      bool
	}{
		{"anything", "", true}, // empty prefix disables the filter
		{"", "", true},
		{"ab", "abc", false}, // candidate shorter than the prefix
		{"abc", "abc", true},
		{"abcdef", "abc", true},
		{"xbcdef", "abc", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, hasPrefix(tc.s, tc.prefix), "hasPrefix(%q, %q)", tc.s, tc.prefix)
	}
}

// TestComplete_ForwardsRequestAndPropagatesError proves the router is a
// pass-through: the child sees the identical request, and a child error
// is returned verbatim rather than swallowed or retried elsewhere.
func TestComplete_ForwardsRequestAndPropagatesError(t *testing.T) {
	boom := errors.New("upstream exploded")
	child := &fakeProvider{name: "haiku", err: boom}
	fallback := &fakeProvider{name: "sonnet"}

	r, err := New(Options{
		Default:   "sonnet",
		Providers: map[string]agent.Provider{"sonnet": fallback, "haiku": child},
		Rules:     []Rule{{MessageLenMax: 10, Use: "haiku"}},
		Logger:    slog.New(slog.NewTextHandler(newDiscard(), nil)),
	})
	require.NoError(t, err)

	req := agent.Request{SessionID: "s1", System: "sys", Messages: []agent.Message{userMsg("hi")}}
	_, err = r.Complete(context.Background(), req)
	require.ErrorIs(t, err, boom)

	require.Len(t, child.got, 1)
	assert.Equal(t, req, child.got[0], "child must receive the untouched request")
	assert.Empty(t, fallback.got, "fallback must not be consulted after a rule match")
}
