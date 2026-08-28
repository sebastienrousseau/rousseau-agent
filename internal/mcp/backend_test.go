package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func openBackend(t *testing.T) (SessionsBackend, *sqlitestore.Store, *sqlitestore.CronStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	cs, err := sqlitestore.NewCronStore(context.Background(), s)
	require.NoError(t, err)
	return NewStoreBackend(s, cs), s, cs
}

func TestStoreBackend_SearchRoundtrip(t *testing.T) {
	be, s, _ := openBackend(t)
	sess := agent.NewSession("about kubernetes")
	sess.Append(agent.NewUserText("pods and services and helm charts"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := be.Search(context.Background(), "helm", sqlitestore.SearchOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, hits)
}

func TestStoreBackend_ListRoundtrip(t *testing.T) {
	be, s, _ := openBackend(t)
	sess := agent.NewSession("list me")
	require.NoError(t, s.Save(context.Background(), sess))

	summaries, err := be.List(context.Background(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, summaries)
}

func TestStoreBackend_LoadRoundtrip(t *testing.T) {
	be, s, _ := openBackend(t)
	sess := agent.NewSession("load me")
	sess.Append(agent.NewUserText("hi"))
	require.NoError(t, s.Save(context.Background(), sess))

	got, err := be.Load(context.Background(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, got.ID)
}

func TestStoreBackend_CronListRoundtrip(t *testing.T) {
	be, _, cs := openBackend(t)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID: "1", Name: "test", CronExpr: "0 * * * *", Prompt: "p", DeliverTo: "u", Enabled: true,
	}))
	jobs, err := be.CronList(context.Background())
	require.NoError(t, err)
	assert.Len(t, jobs, 1)
}

func TestStoreBackend_CronListNilCron(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	be := NewStoreBackend(s, nil)
	jobs, err := be.CronList(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs)
}
