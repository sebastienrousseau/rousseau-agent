package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// TestNotifyInterruptedTurns pins the restart notice: assembleDaemon
// wires a journal on SQLite, a turn left open by the previous process
// is delivered to its sender once, quoting the message start, and
// other transports' entries are left alone.
func TestNotifyInterruptedTurns(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup
	require.NotNil(t, wiring.TurnJournal, "SQLite deployments get a journal")

	ctx := context.Background()
	require.NoError(t, wiring.TurnJournal.Begin(ctx, "whatsapp", "a@s.whatsapp.net", "deploy staging"))
	require.NoError(t, wiring.TurnJournal.Begin(ctx, "telegram", "42", "other"))

	type sent struct{ to, body string }
	var got []sent
	deliver := func(_ context.Context, to, body string) error {
		got = append(got, sent{to, body})
		return nil
	}
	notifyInterruptedTurns(ctx, wiring.TurnJournal, "whatsapp", nil, deliver, silentLogger())
	notifyInterruptedTurns(ctx, wiring.TurnJournal, "whatsapp", nil, deliver, silentLogger())

	require.Len(t, got, 1, "each interrupted turn is announced once")
	assert.Equal(t, "a@s.whatsapp.net", got[0].to)
	assert.Contains(t, got[0].body, `"deploy staging"`)
	assert.Contains(t, got[0].body, "restarted")

	left, err := wiring.TurnJournal.TakeInterrupted(ctx, "telegram")
	require.NoError(t, err)
	assert.Len(t, left, 1)
}
