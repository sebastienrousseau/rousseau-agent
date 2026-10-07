package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestInterruptedNotice(t *testing.T) {
	none := interruptedNotice("deploy", nil)
	assert.Contains(t, none, `("deploy")`)
	assert.Contains(t, none, "Nothing had run yet")

	got := interruptedNotice("deploy", []agent.LedgerEntry{
		{Tool: "bash", Detail: "make deploy"},
		{Tool: "write", Detail: "/tmp/n.md", Failed: true},
		{Tool: "spawn"},
	})
	assert.Contains(t, got, "Before the restart I had run:")
	assert.Contains(t, got, "\n- bash make deploy\n")
	assert.Contains(t, got, "\n- write /tmp/n.md (failed or unfinished)\n")
	assert.Contains(t, got, "\n- spawn\n")
	assert.True(t, strings.HasSuffix(got, "Ask me what state things are in before sending it again."))

	var many []agent.LedgerEntry
	for i := 0; i < maxLedgerLines+3; i++ {
		many = append(many, agent.LedgerEntry{Tool: fmt.Sprintf("t%d", i)})
	}
	long := interruptedNotice("x", many)
	assert.Contains(t, long, "- …and 3 more")
	assert.NotContains(t, long, fmt.Sprintf("- t%d", maxLedgerLines))
}

// End to end through the daemon wiring: a turn checkpointed with a tool
// call, then interrupted, produces a notice that names the tool call.
func TestNotifyInterruptedTurns_ListsCheckpointedSideEffects(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	ctx := context.Background()
	wiring, err := assembleDaemon(ctx, opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	sess := agent.NewSession("chat")
	sess.Sender = "whatsapp:a@s.whatsapp.net"
	sess.Append(agent.NewUserText("clean the build"))
	sess.Append(agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{
		{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`)}},
	}})
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{
		{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{ToolUseID: "t1", Output: "ok"}},
	}})
	require.NoError(t, wiring.Sessions.Save(ctx, sess))
	require.NoError(t, wiring.JIDMap.Put(ctx, "whatsapp:a@s.whatsapp.net", sess.ID))
	require.NoError(t, wiring.TurnJournal.Begin(ctx, "whatsapp", "a@s.whatsapp.net", "clean the build"))

	var body string
	notifyInterruptedTurns(ctx, wiring.TurnJournal, "whatsapp", wiring.TurnLedger("whatsapp"),
		func(_ context.Context, _, b string) error { body = b; return nil }, silentLogger())
	assert.Contains(t, body, "- bash rm -rf build")

	// An unknown sender has an empty ledger rather than a new session.
	assert.Empty(t, wiring.TurnLedger("whatsapp")(ctx, "nobody"))
	assert.Empty(t, (&daemonWiring{}).TurnLedger("whatsapp")(ctx, "a"))
}
