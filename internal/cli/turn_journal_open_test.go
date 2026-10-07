package cli

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// openTurnJournal dispatches on the store's driver; on SQLite the
// journal round-trips Begin/TakeInterrupted, and notifyInterruptedTurns
// delivers one notice per interrupted sender.
func TestOpenTurnJournal_SQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := openSearchableStore(ctx, config.StateConfig{Path: t.TempDir() + "/s.db"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	j, err := openTurnJournal(ctx, store)
	require.NoError(t, err)
	require.NoError(t, j.Begin(ctx, "whatsapp", "alice", "plan the trip"))

	var sent []string
	notifyInterruptedTurns(ctx, j, "whatsapp", restartRecovery{}, func(_ context.Context, to, body string) error {
		sent = append(sent, to+"|"+body)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "alice|")
	assert.Contains(t, sent[0], "plan the trip")
}

func TestOpenTurnJournal_UnknownStoreIsNilInterface(t *testing.T) {
	j, err := openTurnJournal(context.Background(), nil)
	assert.Error(t, err)
	assert.Nil(t, j, "a failed open must not return a typed nil the daemon would call")
	notifyInterruptedTurns(context.Background(), j, "x", restartRecovery{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
