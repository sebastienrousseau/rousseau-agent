package health

import (
	"context"
	"path/filepath"
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
