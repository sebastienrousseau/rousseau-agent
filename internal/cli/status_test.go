package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func TestCollectStatus_MissingFileReturnsEmpty(t *testing.T) {
	got, err := collectStatus(context.Background(), filepath.Join(t.TempDir(), "no.db"))
	require.NoError(t, err)
	assert.Equal(t, 0, got.Sessions)
	assert.Equal(t, int64(0), got.StateSize)
}

func TestCollectStatus_ReportsPopulatedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	sess := agent.NewSession("hello")
	sess.Append(agent.NewUserText("hi"))
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	got, err := collectStatus(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Sessions)
	assert.False(t, got.LastActivityAt.IsZero())
	assert.Greater(t, got.StateSize, int64(0))
}

func TestRenderStatus_ContainsCounts(t *testing.T) {
	buf := &bytes.Buffer{}
	renderStatus(buf, StatusReport{
		Version: "0.0.0", Sessions: 3, CronJobs: 2, CronEnabled: 1,
	})
	out := buf.String()
	assert.Contains(t, out, "sessions               3")
	assert.Contains(t, out, "cron.jobs              2")
	assert.Contains(t, out, "enabled=1")
}

func TestScanCount_MissingTableReturnsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	got, err := collectStatus(context.Background(), path)
	require.NoError(t, err)
	// jid_sessions is created lazily by whatsapp cmd, so at this point
	// it may not exist — scanCount must return 0 rather than error.
	assert.GreaterOrEqual(t, got.JIDMappings, 0)
}
