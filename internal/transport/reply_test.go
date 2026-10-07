package transport

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitReply_PassThrough(t *testing.T) {
	assert.Nil(t, SplitReply("", 10))
	assert.Equal(t, []string{"short"}, SplitReply("short", 10))
	assert.Equal(t, []string{"no limit"}, SplitReply("no limit", 0))
	assert.Equal(t, []string{"exactly10!"}, SplitReply("exactly10!", 10))
}

func TestSplitReply_PrefersParagraphThenLineThenSpace(t *testing.T) {
	text := "para one line\n\npara two\nline b\nline c tail words"
	got := SplitReply(text, 24)
	assert.Equal(t, "para one line", got[0], "blank line in the back half wins")
	for _, p := range got {
		assert.LessOrEqual(t, len(p), 24)
	}
	assert.Equal(t, strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(strings.Join(got, " ")), " "), "no words lost")

	got = SplitReply("alpha beta gamma delta epsilon", 12)
	assert.Equal(t, []string{"alpha beta", "gamma delta", "epsilon"}, got)
}

func TestSplitReply_HardCutStaysOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", 50) // 100 bytes, no separators
	got := SplitReply(text, 15)
	require.Len(t, got, 8)
	for _, p := range got {
		assert.True(t, utf8.ValidString(p))
		assert.LessOrEqual(t, len(p), 15)
	}
	assert.Equal(t, text, strings.Join(got, ""))
}

func TestSplitReply_LimitSmallerThanRune(t *testing.T) {
	got := SplitReply("€€", 1)
	assert.Equal(t, []string{"€", "€"}, got)
}

func TestSplitReply_IgnoresEarlySeparator(t *testing.T) {
	// A newline in the front half must not yield a one-word piece.
	text := "hi\n" + strings.Repeat("x", 30)
	got := SplitReply(text, 20)
	assert.Len(t, got[0], 20)
}
