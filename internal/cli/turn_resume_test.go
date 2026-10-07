package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestRecoverTurn(t *testing.T) {
	ctx := context.Background()
	ledger := func(context.Context, string) []agent.LedgerEntry { return []agent.LedgerEntry{{Tool: "bash"}} }

	body, outcome := recoverTurn(ctx, restartRecovery{
		ledger: ledger,
		resume: func(context.Context, string) (string, error) { return "done it", nil },
	}, "a", "p", silentLogger())
	assert.Equal(t, "resumed", outcome)
	assert.Equal(t, resumedPrefix+"done it", body)

	body, outcome = recoverTurn(ctx, restartRecovery{
		ledger: ledger,
		resume: func(context.Context, string) (string, error) { return "", errors.New("provider down") },
	}, "a", "p", silentLogger())
	assert.Equal(t, "notified", outcome, "a failed resume falls back to the notice")
	assert.Contains(t, body, "- bash")

	_, outcome = recoverTurn(ctx, restartRecovery{
		resume: func(context.Context, string) (string, error) { return "", nil },
	}, "a", "p", silentLogger())
	assert.Equal(t, "notified", outcome, "an empty reply is not delivered as a resume")

	body, outcome = recoverTurn(ctx, restartRecovery{}, "a", "p", silentLogger())
	assert.Equal(t, "notified", outcome)
	assert.Contains(t, body, "Nothing had run yet")
}

func resumeWiring(t *testing.T, resume bool, timeout time.Duration) *daemonWiring {
	t.Helper()
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	opts.Config.Agent.ResumeInterrupted = resume
	opts.Config.Agent.TurnTimeout = timeout
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = wiring.Cleanup() }) //nolint:errcheck // test cleanup
	return wiring
}

func TestRestartRecovery_ResumeIsOptIn(t *testing.T) {
	off := resumeWiring(t, false, 0).RestartRecovery("whatsapp")
	assert.NotNil(t, off.ledger)
	assert.Nil(t, off.resume, "resume is off by default")

	on := resumeWiring(t, true, time.Minute).RestartRecovery("whatsapp")
	assert.NotNil(t, on.resume)
}

// End to end: a turn that finished and was saved, but whose reply never
// reached the sender, is re-delivered on restart without a model call.
func TestNotifyInterruptedTurns_ResumesFinishedTurn(t *testing.T) {
	ctx := context.Background()
	wiring := resumeWiring(t, true, time.Minute)
	sess := agent.NewSession("chat")
	sess.Sender = "whatsapp:a@s.whatsapp.net"
	sess.Append(agent.NewUserText("summarise the report"))
	sess.Append(agent.NewAssistantText("Here is the summary."))
	require.NoError(t, wiring.Sessions.Save(ctx, sess))
	require.NoError(t, wiring.JIDMap.Put(ctx, "whatsapp:a@s.whatsapp.net", sess.ID))
	require.NoError(t, wiring.TurnJournal.Begin(ctx, "whatsapp", "a@s.whatsapp.net", "summarise the report"))

	var to, body string
	notifyInterruptedTurns(ctx, wiring.TurnJournal, "whatsapp", wiring.RestartRecovery("whatsapp"),
		func(_ context.Context, t, b string) error { to, body = t, b; return nil }, silentLogger())
	assert.Equal(t, "a@s.whatsapp.net", to)
	assert.Equal(t, resumedPrefix+"Here is the summary.", body)
}
