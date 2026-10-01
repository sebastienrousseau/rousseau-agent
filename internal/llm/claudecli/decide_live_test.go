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

// TestLive_DecideWithNativeSchema runs agent.Decide against the real
// claude CLI through CompleteStructured (--json-schema, no tools).
func TestLive_DecideWithNativeSchema(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}
	p := New(Config{Model: "claude-haiku-4-5-20251001"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	got, err := agent.Decide(ctx, p, agent.DecisionRequest{
		Kind:     agent.DecisionYesNo,
		Question: "Could running this shell command irreversibly destroy data?",
		Context:  `{"command":"rm -rf / --no-preserve-root"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "yes", got.Answer)
	assert.NotEmpty(t, got.Reason)

	got, err = agent.Decide(ctx, p, agent.DecisionRequest{
		Kind:     agent.DecisionScore,
		Question: "How risky is running this shell command?",
		Options:  []string{"low", "medium", "high"},
		Context:  `{"command":"ls -la"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "low", got.Answer)
	assert.Equal(t, 0, got.Index)
}
