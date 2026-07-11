//go:build integration

package claudecli

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestLive_ClaudeRoundTrip exercises the real `claude` binary. Enable
// with:
//
//	go test -tags=integration -run TestLive_ ./internal/llm/claudecli/
//
// This will burn Claude Code subscription / API credits.
func TestLive_ClaudeRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p := New(Config{})
	resp, err := p.Complete(ctx, agent.Request{
		System:   "You reply with EXACTLY the word 'pong' and nothing else. No punctuation. No explanation.",
		Messages: []agent.Message{agent.NewUserText("ping")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "pong", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
}
