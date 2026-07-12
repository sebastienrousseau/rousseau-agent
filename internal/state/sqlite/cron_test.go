package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openCronTestStore(t *testing.T) *CronStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cron.db")
	s, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	cs, err := NewCronStore(context.Background(), s)
	require.NoError(t, err)
	return cs
}

func TestCronStore_Put_RequiresAllFields(t *testing.T) {
	cs := openCronTestStore(t)
	err := cs.Put(context.Background(), CronJob{ID: "x"})
	assert.Error(t, err)
}

func TestCronStore_PutAndList(t *testing.T) {
	cs := openCronTestStore(t)
	require.NoError(t, cs.Put(context.Background(), CronJob{
		ID: "1", Name: "morning", CronExpr: "0 8 * * *", Prompt: "hi",
		DeliverTo: "user@example", Enabled: true,
	}))
	jobs, err := cs.List(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "morning", jobs[0].Name)
	assert.True(t, jobs[0].Enabled)
	assert.Nil(t, jobs[0].LastRunAt)
}

func TestCronStore_SetEnabled(t *testing.T) {
	cs := openCronTestStore(t)
	require.NoError(t, cs.Put(context.Background(), CronJob{
		ID: "1", Name: "off-me", CronExpr: "0 8 * * *", Prompt: "x",
	}))
	require.NoError(t, cs.SetEnabled(context.Background(), "off-me", false))
	jobs, err := cs.List(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.False(t, jobs[0].Enabled)
}

func TestCronStore_Delete(t *testing.T) {
	cs := openCronTestStore(t)
	require.NoError(t, cs.Put(context.Background(), CronJob{
		ID: "1", Name: "gone", CronExpr: "0 8 * * *", Prompt: "x",
	}))
	require.NoError(t, cs.Delete(context.Background(), "gone"))
	jobs, err := cs.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestCronStore_RecordRun(t *testing.T) {
	cs := openCronTestStore(t)
	require.NoError(t, cs.Put(context.Background(), CronJob{
		ID: "j-1", Name: "runme", CronExpr: "0 8 * * *", Prompt: "x", Enabled: true,
	}))
	now := time.Now().UTC()
	require.NoError(t, cs.RecordRun(context.Background(), "j-1", now))
	jobs, err := cs.List(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NotNil(t, jobs[0].LastRunAt)
	assert.WithinDuration(t, now, *jobs[0].LastRunAt, time.Second)
}

func TestCronStore_UniqueName(t *testing.T) {
	cs := openCronTestStore(t)
	require.NoError(t, cs.Put(context.Background(), CronJob{
		ID: "1", Name: "dup", CronExpr: "0 8 * * *", Prompt: "x",
	}))
	err := cs.Put(context.Background(), CronJob{
		ID: "2", Name: "dup", CronExpr: "0 9 * * *", Prompt: "y",
	})
	assert.Error(t, err)
}
