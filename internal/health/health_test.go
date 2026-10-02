package health

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	p := Path(dir, "whatsapp")
	now := time.Now().UTC()
	up, down := true, false

	err := Check(p, time.Minute, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no heartbeat")

	require.NoError(t, Write(p, Beat{Transport: "whatsapp", UpdatedAt: now, Connected: &up}))
	assert.NoError(t, Check(p, time.Minute, now))

	require.NoError(t, Write(p, Beat{Transport: "whatsapp", UpdatedAt: now, Connected: &down}))
	assert.ErrorContains(t, Check(p, time.Minute, now), "not connected")

	require.NoError(t, Write(p, Beat{Transport: "whatsapp", UpdatedAt: now.Add(-5 * time.Minute), Connected: &up}))
	assert.ErrorContains(t, Check(p, time.Minute, now), "stale")

	// No connection notion: freshness alone decides.
	require.NoError(t, Write(p, Beat{Transport: "email", UpdatedAt: now}))
	assert.NoError(t, Check(p, time.Minute, now))
}

func TestRun_WritesBeatsUntilCancelled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "health", "slack.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	connected := false
	go func() {
		Run(ctx, p, "slack", func() (bool, bool) { return connected, true }, 10*time.Millisecond)
		close(done)
	}()
	assert.Eventually(t, func() bool {
		err := Check(p, time.Second, time.Now())
		return err != nil && assert.ObjectsAreEqual("slack is not connected upstream", err.Error())
	}, time.Second, 5*time.Millisecond)
	cancel()
	<-done
}

func TestWrite_Errors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	assert.ErrorContains(t, Write(filepath.Join(blocker, "x.json"), Beat{}), "health: dir")

	p := filepath.Join(dir, "beat.json")
	require.NoError(t, os.Mkdir(p+".tmp", 0o700))
	assert.ErrorContains(t, Write(p, Beat{}), "health: write")
}

func TestCheck_UnreadableAndMalformed(t *testing.T) {
	dir := t.TempDir()
	assert.ErrorContains(t, Check(dir, time.Minute, time.Now()), "read heartbeat")

	p := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(p, []byte("{"), 0o600))
	assert.ErrorContains(t, Check(p, time.Minute, time.Now()), "malformed heartbeat")
}

func TestRun_BeatsOnEachTick(t *testing.T) {
	p := filepath.Join(t.TempDir(), "beat.json")
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Run(ctx, p, "signal", func() (bool, bool) { calls.Add(1); return false, false }, 5*time.Millisecond)
		close(done)
	}()
	assert.Eventually(t, func() bool { return calls.Load() >= 3 }, time.Second, 5*time.Millisecond)
	cancel()
	<-done
	assert.NoError(t, Check(p, time.Minute, time.Now()), "no connection notion: a fresh beat is healthy")
}
