package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// A reply the model could not finish is delivered with a visible
// marker; the session keeps the model's exact output.
func TestTurn_MaxTokensIsMarkedForTheSender(t *testing.T) {
	prov := &stubProvider{responses: []Response{{
		Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "The answer is long and"}}},
		StopReason: StopMaxTokens,
		Usage:      Usage{OutputTokens: 4096},
	}}}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("explain everything"))

	final, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, final.Content, 2)
	assert.Equal(t, "The answer is long and", final.Content[0].Text)
	assert.Equal(t, TruncationMarker, final.Content[1].Text)
	assert.True(t, strings.Contains(TruncationMarker, "cut off"))

	// Stored message is untouched.
	require.Len(t, s.Messages, 2)
	assert.Len(t, s.Messages[1].Content, 1)
}

func TestTurn_EndTurnAndOtherAreNotMarked(t *testing.T) {
	for _, reason := range []StopReason{StopEndTurn, StopOther} {
		prov := &stubProvider{responses: []Response{{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "done"}}},
			StopReason: reason,
		}}}
		a := New(prov, tools.NewRegistry(), silentLogger(), Options{})
		s := NewSession("x")
		s.Append(NewUserText("hi"))
		final, err := a.Turn(context.Background(), s)
		require.NoError(t, err)
		require.Len(t, final.Content, 1, "%s", reason)
		assert.Equal(t, "done", final.Content[0].Text)
	}
}
