package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(rules []Rule) (*Handler, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	inner := slog.NewJSONHandler(buf, nil)
	return New(inner, rules), buf
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	return m
}

func TestHandler_RedactsAnthropicKey(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("provider.call", slog.String("body", "Authorization: Bearer sk-ant-"+strings.Repeat("A", 90)))
	got := decode(t, buf)
	assert.Contains(t, got["body"], "«redacted:anthropic»")
	assert.NotContains(t, got["body"], "sk-ant-")
}

func TestHandler_RedactsOpenAIKey(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call", slog.String("k", "sk-"+strings.Repeat("z", 48)))
	got := decode(t, buf)
	assert.Contains(t, got["k"], "«redacted:openai»")
}

func TestHandler_RedactsSlackTokens(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call",
		slog.String("bot", "xoxb-1234-5678-abc"),
		slog.String("app", "xapp-1-A-1-token"),
	)
	got := decode(t, buf)
	assert.Contains(t, got["bot"], "«redacted:slack»")
	assert.Contains(t, got["app"], "«redacted:slack»")
}

func TestHandler_RedactsGitHubPAT(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call",
		slog.String("classic", "ghp_"+strings.Repeat("a", 36)),
		slog.String("fine", "github_pat_"+strings.Repeat("z", 82)),
	)
	got := decode(t, buf)
	assert.Contains(t, got["classic"], "«redacted:github»")
	assert.Contains(t, got["fine"], "«redacted:github»")
}

func TestHandler_RedactsAWSAndJWT(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call",
		slog.String("aws", "AKIAIOSFODNN7EXAMPLE"),
		slog.String("jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdef123456"),
	)
	got := decode(t, buf)
	assert.Contains(t, got["aws"], "«redacted:aws»")
	assert.Contains(t, got["jwt"], "«redacted:jwt»")
}

func TestHandler_RedactsByKeyName(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call",
		slog.String("password", "not-shaped-like-a-known-secret-but-still-scrubbable"),
		slog.String("api_key", "kbd-cust"),
		slog.String("Authorization", "Bearer abcdefghijklmnop"),
	)
	got := decode(t, buf)
	assert.Contains(t, got["password"], "«redacted:key»")
	assert.Contains(t, got["api_key"], "«redacted:key»")
	assert.Contains(t, got["Authorization"], "«redacted:key»")
}

func TestHandler_LeavesInnocuousAttrsAlone(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("hello", slog.String("user", "alice"), slog.Int("count", 42))
	got := decode(t, buf)
	assert.Equal(t, "alice", got["user"])
	assert.EqualValues(t, 42, got["count"])
}

func TestHandler_PhoneRuleOptIn(t *testing.T) {
	rules := append(DefaultRules(), PhoneRule())
	h, buf := newHandler(rules)
	logger := slog.New(h)
	logger.Info("dial", slog.String("to", "+15551234567"))
	got := decode(t, buf)
	assert.Contains(t, got["to"], "«redacted:phone»")

	// Default rules alone don't redact phones.
	h2, buf2 := newHandler(DefaultRules())
	slog.New(h2).Info("dial", slog.String("to", "+15551234567"))
	got2 := decode(t, buf2)
	assert.Equal(t, "+15551234567", got2["to"])
}

func TestHandler_WithAttrsScrubsPreBound(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h).With(slog.String("token", "ghp_"+strings.Repeat("a", 36)))
	logger.Info("hi")
	got := decode(t, buf)
	assert.Contains(t, got["token"], "«redacted:")
}

func TestHandler_GroupsAreScrubbed(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h)
	logger.Info("call", slog.Group("headers",
		slog.String("authorization", "Bearer abcdefghij"),
		slog.String("x-request-id", "req-123"),
	))
	got := decode(t, buf)
	hdrs := got["headers"].(map[string]any)
	assert.Contains(t, hdrs["authorization"], "«redacted:")
	assert.Equal(t, "req-123", hdrs["x-request-id"])
}

func TestHandler_ZeroRulesIsNoop(t *testing.T) {
	h, buf := newHandler(nil)
	logger := slog.New(h)
	logger.Info("call", slog.String("token", "ghp_"+strings.Repeat("a", 36)))
	got := decode(t, buf)
	assert.NotContains(t, got["token"], "«redacted:")
}

func TestHandler_EnabledDelegates(t *testing.T) {
	h, _ := newHandler(DefaultRules())
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}

func TestHandler_WithGroup(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	logger := slog.New(h).WithGroup("outer")
	logger.Info("call", slog.String("token", "ghp_"+strings.Repeat("a", 36)))
	got := decode(t, buf)
	outer := got["outer"].(map[string]any)
	assert.Contains(t, outer["token"], "«redacted:")
}

// TestProperty_neverPanics fuzz-checks that no input string, however
// pathological, causes a panic in the rule pipeline.
func TestProperty_neverPanics(t *testing.T) {
	h, _ := newHandler(append(DefaultRules(), PhoneRule()))
	inputs := []string{
		"", "a", "aa", "\x00\x00\x00", "🚀🚀🚀🚀🚀🚀",
		strings.Repeat("A", 10000),
		strings.Repeat("\n", 1000),
		"AKIA" + strings.Repeat("Z", 16) + "trailing",
		"eyJ.eyJ.short",
	}
	logger := slog.New(h)
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", in, r)
				}
			}()
			logger.Info("t", slog.String("value", in))
		}()
	}
}
