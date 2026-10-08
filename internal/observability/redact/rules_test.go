package redact

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every fixture below is a fake, built by concatenation so secret
// scanners do not flag the source.
func TestRedact_TokenTable(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		class  string
	}{
		{"openai-proj", "sk-" + "proj-" + strings.Repeat("Ab1_-", 10), "openai"},
		{"openai-svcacct", "sk-" + "svcacct-" + strings.Repeat("Ab1_-", 10), "openai"},
		{"openai-admin", "sk-" + "admin-" + strings.Repeat("Ab1_-", 10), "openai"},
		{"slack-xoxp", "xox" + "p-1234567890-1234567890-abcdefFAKE", "slack"},
		{"slack-xoxe", "xox" + "e-1-FAKEFAKEFAKEFAKE", "slack"},
		{"slack-xoxa", "xox" + "a-2-FAKEFAKEFAKEFAKE", "slack"},
		{"slack-xoxr", "xox" + "r-FAKEFAKEFAKEFAKE", "slack"},
		{"slack-xoxs", "xox" + "s-FAKEFAKEFAKEFAKE", "slack"},
		{"github-ghp", "gh" + "p_" + strings.Repeat("a", 36), "github"},
		{"github-gho", "gh" + "o_" + strings.Repeat("b", 36), "github"},
		{"github-ghu", "gh" + "u_" + strings.Repeat("c", 36), "github"},
		{"github-ghs", "gh" + "s_" + strings.Repeat("d", 36), "github"},
		{"github-ghr", "gh" + "r_" + strings.Repeat("e", 36), "github"},
		{"telegram", "123456789" + ":" + "AAFAKE" + strings.Repeat("x", 29), "telegram"},
		{"google-oauth", "ya29" + "." + strings.Repeat("FakeTok_-", 4), "google"},
		{"bearer", "Bearer " + strings.Repeat("fAkE", 6), "bearer"},
		{"bearer-lower", "bearer " + strings.Repeat("fAkE", 6), "bearer"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIFAKE\nFAKEFAKE\n-----END RSA PRIVATE KEY-----", "private-key"},
		{"pem-plain", "-----BEGIN PRIVATE KEY-----\nMIIFAKE\n-----END PRIVATE KEY-----", "private-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newHandler(DefaultRules())
			slog.New(h).Info("m", slog.String("body", "before "+tc.secret+" after"))
			got := decode(t, buf)["body"].(string)
			assert.Contains(t, got, "«redacted:"+tc.class+"»")
			assert.NotContains(t, got, tc.secret)
			assert.True(t, strings.HasPrefix(got, "before "), got)
			assert.True(t, strings.HasSuffix(got, " after"), got)
		})
	}
}

func TestRedact_URLUserinfoPassword(t *testing.T) {
	h, buf := newHandler(DefaultRules())
	slog.New(h).Info("m", slog.String("dsn", "postgres://alice:"+"FAKEpw123@db.example:5432/app"))
	got := decode(t, buf)["dsn"].(string)
	assert.Equal(t, "postgres://alice:«redacted»@db.example:5432/app", got)
}

func TestRedact_TwoSecretsInOneValue(t *testing.T) {
	gh := "gh" + "p_" + strings.Repeat("a", 36)
	aws := "AKIA" + "FAKEFAKEFAKEFAKE"
	h, buf := newHandler(DefaultRules())
	slog.New(h).Info("m", slog.String("body", gh+" and "+aws))
	got := decode(t, buf)["body"].(string)
	assert.NotContains(t, got, gh)
	assert.NotContains(t, got, aws)
	assert.Equal(t, "«redacted:github» and «redacted:aws»", got)
}

func TestRedact_MessageScrubbed(t *testing.T) {
	aws := "AKIA" + "FAKEFAKEFAKEFAKE"
	h, buf := newHandler(DefaultRules())
	slog.New(h).Info("leaked " + aws)
	got := decode(t, buf)["msg"].(string)
	assert.Equal(t, "leaked «redacted:aws»", got)
}

type fakeValuer struct{ secret string }

func (f fakeValuer) LogValue() slog.Value { return slog.StringValue("v=" + f.secret) }

type fakeGroupValuer struct{ secret string }

func (f fakeGroupValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("inner", f.secret), slog.String("password", "hunter2fake"))
}

func TestRedact_LogValuerResolved(t *testing.T) {
	aws := "AKIA" + "FAKEFAKEFAKEFAKE"
	h, buf := newHandler(DefaultRules())
	slog.New(h).Info("m",
		slog.Any("lv", fakeValuer{secret: aws}),
		slog.Any("grp", fakeGroupValuer{secret: aws}),
	)
	got := decode(t, buf)
	assert.Equal(t, "v=«redacted:aws»", got["lv"])
	grp := got["grp"].(map[string]any)
	assert.Equal(t, "«redacted:aws»", grp["inner"])
	assert.Equal(t, "«redacted:key»", grp["password"])
}

func TestRedact_NoFalsePositives(t *testing.T) {
	plain := []string{
		"token budget exceeded: 4096 of 8192",
		"request 550e8400-e29b-41d4-a716-446655440000 done",
		"the bearer of bad news",
		"see https://example.com/path?q=1",
		"12:30:45 meeting at room 4",
		"ask-ant-colony",
	}
	for _, p := range plain {
		h, buf := newHandler(DefaultRules())
		slog.New(h).Info(p, slog.String("body", p))
		got := decode(t, buf)
		assert.Equal(t, p, got["body"])
		assert.Equal(t, p, got["msg"])
	}
}

func TestString_UsesDefaultValueRules(t *testing.T) {
	in := "export X=sk-" + "ant-api03-FAKEFAKEFAKEFAKEFAKEFAKE"
	got := String(in)
	assert.NotContains(t, got, "FAKEFAKE")
	assert.Contains(t, got, "«redacted:anthropic»")
	assert.Equal(t, "token budget", String("token budget"))
}
