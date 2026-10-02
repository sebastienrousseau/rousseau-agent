package senderkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakeAndSplit(t *testing.T) {
	assert.Equal(t, "signal:+447700900123", Make("signal", "+447700900123"))
	assert.Equal(t, "+447700900123", Make("", "+447700900123"), "no transport: unchanged")
	assert.Equal(t, "", Make("signal", ""), "no sender: unchanged")

	tp, s, ok := Split("matrix:@ann:example.org")
	assert.True(t, ok)
	assert.Equal(t, "matrix", tp)
	assert.Equal(t, "@ann:example.org", s, "only the first colon separates")

	_, s, ok = Split("@ann:example.org")
	assert.False(t, ok, "a bare Matrix id is not a key")
	assert.Equal(t, "@ann:example.org", s)
	_, _, ok = Split("unknown:x")
	assert.False(t, ok)
	assert.Equal(t, "U0123ABCD", Bare("slack:U0123ABCD"))
	assert.Equal(t, "U0123ABCD", Bare("U0123ABCD"))
}

func TestInfer(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		candidates []string
	}{
		{"447700900123@s.whatsapp.net", "whatsapp", []string{"whatsapp"}},
		{"276540210315282@lid", "whatsapp", []string{"whatsapp"}},
		{"120363000000000000@g.us", "whatsapp", []string{"whatsapp"}},
		{"@ann:example.org", "matrix", []string{"matrix"}},
		{"U0123ABCD", "slack", []string{"slack"}},
		{"+447700900123", "", []string{"imessage", "signal"}},
		{"ann@example.com", "", []string{"email", "imessage"}},
		{"123456789", "", []string{"discord", "telegram"}},
		{"-1001234567890", "", []string{"discord", "telegram"}},
		{"something else", "", nil},
	}
	for _, c := range cases {
		got, candidates, ok := Infer(c.in)
		assert.Equal(t, c.want, got, c.in)
		assert.Equal(t, c.candidates, candidates, c.in)
		assert.Equal(t, c.want != "", ok, c.in)
	}
}

func TestPlanKeys(t *testing.T) {
	p, err := PlanKeys([]string{
		"+447700900123",
		"447700900123@s.whatsapp.net",
		"signal:+447700900999",
		"ann@example.com",
	}, map[string]string{"+447700900123": "signal"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"+447700900123":               "signal:+447700900123",
		"447700900123@s.whatsapp.net": "whatsapp:447700900123@s.whatsapp.net",
	}, p.Keys, "already-namespaced keys are left alone")
	assert.Equal(t, []Change{
		{From: "+447700900123", To: "signal:+447700900123", Rule: "map"},
		{From: "447700900123@s.whatsapp.net", To: "whatsapp:447700900123@s.whatsapp.net", Rule: "shape"},
	}, p.Changes)
	assert.Equal(t, []Ambiguous{{Sender: "ann@example.com", Candidates: []string{"email", "imessage"}}}, p.Ambiguous)

	_, err = PlanKeys([]string{"+447700900123"}, map[string]string{"+447700900123": "carrier-pigeon"})
	assert.ErrorContains(t, err, "unknown transport")
}
