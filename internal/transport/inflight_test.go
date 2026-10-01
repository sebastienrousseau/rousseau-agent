package transport

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestInflight_RunsConcurrentlyAndWaits(t *testing.T) {
	var f Inflight
	release := make(chan struct{})
	var started, done atomic.Int32
	for i := 0; i < 3; i++ {
		f.Go(func() {
			started.Add(1)
			<-release
			done.Add(1)
		})
	}
	assert.Eventually(t, func() bool { return started.Load() == 3 }, time.Second, time.Millisecond,
		"Go must not block the caller on earlier work")
	close(release)
	f.Wait()
	assert.Equal(t, int32(3), done.Load())
}

func TestInflight_NilRunsInline(t *testing.T) {
	var f *Inflight
	ran := false
	f.Do(func() { ran = true })
	assert.True(t, ran, "nil Inflight must run inline, before Do returns")
	f.Wait()
}
