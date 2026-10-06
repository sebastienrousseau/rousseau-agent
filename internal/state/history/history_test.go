package history

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

func enc(t *testing.T, words ...string) []Encoded {
	t.Helper()
	var msgs []model.Message
	for _, w := range words {
		msgs = append(msgs, model.Message{Role: model.RoleUser, Content: []model.Content{{Kind: model.ContentText, Text: w}}})
	}
	out, err := EncodeAll(msgs)
	require.NoError(t, err)
	return out
}

func hashes(e []Encoded) []string {
	var out []string
	for _, m := range e {
		out = append(out, m.Hash)
	}
	return out
}

func TestGrew(t *testing.T) {
	stored := enc(t, "a", "b")
	h := ViewHash(stored, 2)
	assert.True(t, Grew(enc(t, "a", "b", "c"), 2, h))
	assert.True(t, Grew(enc(t, "a", "b"), 2, h), "unchanged")
	assert.True(t, Grew(enc(t, "x"), 0, ViewHash(nil, 0)), "first save of an empty view")
	assert.False(t, Grew(enc(t, "a"), 2, h), "shrunk")
	assert.False(t, Grew(enc(t, "a", "z", "c"), 2, h), "rewritten in the middle")
	// The case a last-message check missed: same length, same last
	// message, different first one (a compressor folding one message).
	assert.False(t, Grew(enc(t, "S", "b"), 2, h), "rewritten head, same tail")
}

func TestAlign(t *testing.T) {
	stored := hashes(enc(t, "a", "b", "c", "d"))

	// The compressor: summary + the last two stored messages + a new one.
	a := Align(stored, enc(t, "S", "c", "d", "e"))
	assert.Equal(t, Alignment{Head: 1, Base: 2, AppendFrom: 3}, a)

	// Nothing in common: append the whole view, no stored row in it.
	assert.Equal(t, Alignment{Head: 0, Base: 4, AppendFrom: 0}, Align(stored, enc(t, "x", "y")))

	// The longest run wins over a shorter later one.
	assert.Equal(t, Alignment{Head: 0, Base: 0, AppendFrom: 4}, Align(stored, enc(t, "a", "b", "c", "d")))

	// No stored rows yet.
	assert.Equal(t, Alignment{Head: 0, Base: 0, AppendFrom: 0}, Align(nil, enc(t, "x")))
}

func TestHeadJSONAndText(t *testing.T) {
	msgs := enc(t, "S", "c")
	assert.Equal(t, "[]", HeadJSON(msgs, Alignment{}))
	assert.Equal(t, "["+string(msgs[0].JSON)+"]", HeadJSON(msgs, Alignment{Head: 1}))

	m := model.Message{Content: []model.Content{
		{Kind: model.ContentText, Text: "hello"},
		{Kind: model.ContentImage, Image: &model.Image{MediaType: "image/png", Data: []byte{1}}},
		{Kind: model.ContentText, Text: "world"},
	}}
	assert.Equal(t, "hello\nworld\n", Text(m))

	_, err := Encode(model.Message{Content: []model.Content{{Kind: model.ContentToolUse,
		ToolUse: &model.ToolUse{Input: []byte("{bad")}}}})
	assert.ErrorContains(t, err, "marshal message")
}
