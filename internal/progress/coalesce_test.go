package progress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var base = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

// at returns base + n seconds; keeps the throttle tables readable.
func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

func TestPolicy_NormaliseFillsZeroKnobsOnly(t *testing.T) {
	got := Policy{MinInterval: 3 * time.Second, MaxUpdates: -1}.Normalise()
	assert.Equal(t, 3*time.Second, got.MinInterval)
	assert.Equal(t, -1, got.MaxUpdates)
	assert.Equal(t, DefaultFirstDelay, got.FirstDelay)
	assert.Equal(t, DefaultMinEditInterval, got.MinEditInterval)
	assert.Equal(t, DefaultHeartbeatInterval, got.HeartbeatInterval)
	assert.Equal(t, DefaultPreviewChars, got.PreviewChars)

	def := DefaultPolicy()
	assert.Equal(t, DefaultMinInterval, def.MinInterval)
	assert.Equal(t, DefaultMaxUpdates, def.MaxUpdates)
}

func TestCoalescer_AbsorbFoldsEveryKind(t *testing.T) {
	c := NewCoalescer("k", Policy{PreviewChars: 10}, base)
	events := []Event{
		{Kind: KindTurnStarted},
		{Kind: KindThinking, Iteration: 1},
		{Kind: KindLLMDelta, Text: "hello world, this is long"},
		{Kind: KindThinking, Iteration: 2},
		{Kind: KindToolStarted, Tool: "bash"},
		{Kind: KindToolStarted, Tool: "read"},
		{Kind: KindToolFinished, Tool: "bash"},
		{Kind: KindToolFinished, Tool: "read", Err: "boom"},
		{Kind: KindToolStarted, Tool: "write"},
		{Kind: KindToolDenied, Tool: "write"},
		{Kind: KindSubagentStarted},
		{Kind: KindSubagentStarted},
		{Kind: KindSubagentFinished},
		{Kind: KindPlanStep, Step: 2, Of: 5},
		{Kind: KindCronStarted, Text: "nightly"},
		{Kind: KindSteered},
		{Kind: KindPaused},
		{Kind: KindResumed},
	}
	for _, ev := range events {
		c.Absorb(ev)
	}

	st := c.State()
	assert.Equal(t, "k", st.Key)
	assert.Equal(t, 2, st.Iteration)
	assert.Empty(t, st.Running)
	assert.Equal(t, 2, st.ToolsDone)
	assert.Equal(t, 1, st.ToolsFailed)
	assert.Equal(t, 1, st.ToolsDenied)
	assert.Equal(t, 1, st.SubagentsRunning)
	assert.Equal(t, 1, st.SubagentsDone)
	assert.Equal(t, 2, st.Step)
	assert.Equal(t, 5, st.Of)
	assert.Equal(t, "nightly", st.Cron)
	assert.Equal(t, 1, st.Steers)
	assert.False(t, st.Paused)
	assert.False(t, st.Terminal)

	// KindThinking clears the streamed preview; the delta that
	// preceded it must not leak into the next round.
	assert.Empty(t, st.Preview)
}

func TestCoalescer_AbsorbEdgeCases(t *testing.T) {
	t.Run("preview is truncated to PreviewChars", func(t *testing.T) {
		c := NewCoalescer("k", Policy{PreviewChars: 5}, base)
		c.Absorb(Event{Kind: KindLLMDelta, Text: "abcdefghij"})
		assert.Equal(t, "fghij", c.State().Preview)
	})
	t.Run("sub-agent finished never goes negative", func(t *testing.T) {
		c := NewCoalescer("k", Policy{}, base)
		c.Absorb(Event{Kind: KindSubagentFinished})
		assert.Equal(t, 0, c.State().SubagentsRunning)
		assert.Equal(t, 1, c.State().SubagentsDone)
	})
	t.Run("cron finished clears the job label", func(t *testing.T) {
		c := NewCoalescer("k", Policy{}, base)
		c.Absorb(Event{Kind: KindCronStarted, Text: "nightly"})
		c.Absorb(Event{Kind: KindCronFinished})
		assert.Empty(t, c.State().Cron)
	})
	t.Run("unknown kinds only mark the state dirty", func(t *testing.T) {
		c := NewCoalescer("k", Policy{}, base)
		c.Absorb(Event{Kind: Kind("mystery")})
		assert.True(t, c.dirty)
	})
	t.Run("terminal clears running tools", func(t *testing.T) {
		c := NewCoalescer("k", Policy{}, base)
		c.Absorb(Event{Kind: KindToolStarted, Tool: "bash"})
		c.Absorb(Event{Kind: KindError, Err: "nope"})
		st := c.State()
		assert.True(t, st.Terminal)
		assert.Equal(t, KindError, st.Outcome)
		assert.Equal(t, "nope", st.Err)
		assert.Empty(t, st.Running)
	})
}

func TestCoalescer_SetDropped(t *testing.T) {
	c := NewCoalescer("k", Policy{}, base)
	c.SetDropped(0)
	assert.False(t, c.dirty, "no change must not dirty the state")
	c.SetDropped(4)
	assert.True(t, c.dirty)
	assert.Equal(t, 4, c.State().Dropped)
}

func TestCoalescer_FirstUpdateWaitsFirstDelay(t *testing.T) {
	c := NewCoalescer("k", Policy{}, base)
	c.Absorb(Event{Kind: KindTurnStarted})

	_, ok := c.Next(at(7))
	assert.False(t, ok, "must stay silent inside FirstDelay")

	u, ok := c.Next(at(8))
	require.True(t, ok)
	assert.Equal(t, 1, u.Seq)
	assert.Equal(t, "k", u.Key)
	assert.Equal(t, 8*time.Second, u.Elapsed)
	assert.False(t, u.Replace, "the first update is always a new message")
	assert.Equal(t, 1, c.Emitted())
}

func TestCoalescer_StaysSilentUntilSomethingHappens(t *testing.T) {
	c := NewCoalescer("k", Policy{}, base)
	_, ok := c.Next(at(600))
	assert.False(t, ok)
}

func TestCoalescer_ThrottleIntervals(t *testing.T) {
	tests := []struct {
		name       string
		preferEdit bool
		// second update attempted at these offsets after the first
		tooSoon, justRight int
	}{
		{"new messages use MinInterval", false, 8 + 24, 8 + 25},
		{"edits use MinEditInterval", true, 8 + 9, 8 + 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCoalescer("k", Policy{PreferEdit: tc.preferEdit}, base)
			c.Absorb(Event{Kind: KindTurnStarted})
			_, ok := c.Next(at(8))
			require.True(t, ok)

			c.Absorb(Event{Kind: KindToolStarted, Tool: "bash"})
			_, ok = c.Next(at(tc.tooSoon))
			assert.False(t, ok)

			u, ok := c.Next(at(tc.justRight))
			require.True(t, ok)
			assert.Equal(t, 2, u.Seq)
			assert.Equal(t, tc.preferEdit, u.Replace)
		})
	}
}

func TestCoalescer_HeartbeatCoversSilentWork(t *testing.T) {
	c := NewCoalescer("k", Policy{PreferEdit: true}, base)
	c.Absorb(Event{Kind: KindToolStarted, Tool: "bash"})
	_, ok := c.Next(at(8))
	require.True(t, ok)

	// Nothing new happens: the edit interval alone is not enough.
	_, ok = c.Next(at(30))
	assert.False(t, ok)
	_, ok = c.Next(at(8 + 89))
	assert.False(t, ok)
	u, ok := c.Next(at(8 + 90))
	require.True(t, ok)
	assert.Equal(t, 2, u.Seq)
}

func TestCoalescer_MaxUpdatesDegradesToHeartbeat(t *testing.T) {
	c := NewCoalescer("k", Policy{
		FirstDelay:      time.Second,
		MinEditInterval: time.Second,
		PreferEdit:      true,
		MaxUpdates:      3,
	}, base)
	now := base
	for i := 0; i < 3; i++ {
		now = now.Add(2 * time.Second)
		c.Absorb(Event{Kind: KindToolStarted, Tool: "bash"})
		_, ok := c.Next(now)
		require.True(t, ok, "update %d", i)
	}
	// Cap reached: new events no longer buy an update.
	c.Absorb(Event{Kind: KindToolStarted, Tool: "read"})
	_, ok := c.Next(now.Add(5 * time.Second))
	assert.False(t, ok)
	// …but the heartbeat still fires.
	_, ok = c.Next(now.Add(DefaultHeartbeatInterval))
	assert.True(t, ok)
}

func TestCoalescer_TerminalFlushesImmediatelyAfterProgress(t *testing.T) {
	c := NewCoalescer("k", Policy{PreferEdit: true}, base)
	c.Absorb(Event{Kind: KindToolStarted, Tool: "bash"})
	_, ok := c.Next(at(8))
	require.True(t, ok)

	c.Absorb(Event{Kind: KindTurnFinished})
	u, ok := c.Next(at(9))
	require.True(t, ok)
	assert.True(t, u.Terminal)
	assert.True(t, u.Replace)
	assert.Contains(t, u.Text, "done in 9s")

	// Terminal fires exactly once.
	_, ok = c.Next(at(10))
	assert.False(t, ok)
}

func TestCoalescer_FastTurnNeverPostsAnything(t *testing.T) {
	// The whole point of FirstDelay: a turn that finishes in 2s must
	// leave no progress message and therefore no epitaph either.
	c := NewCoalescer("k", Policy{}, base)
	c.Absorb(Event{Kind: KindTurnStarted})
	c.Absorb(Event{Kind: KindTurnFinished})
	_, ok := c.Next(at(2))
	assert.False(t, ok)
	_, ok = c.Next(at(3))
	assert.False(t, ok)
	assert.Equal(t, 0, c.Emitted())
}

func TestRemoveFirst(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		drop string
		want []string
	}{
		{"removes first match only", []string{"a", "b", "a"}, "a", []string{"b", "a"}},
		{"absent name is a no-op", []string{"a"}, "z", []string{"a"}},
		{"empty slice", nil, "a", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, removeFirst(tc.in, tc.drop))
		})
	}
}

func TestTail(t *testing.T) {
	assert.Equal(t, "abc", tail("abc", 5))
	assert.Equal(t, "bc", tail("abc", 2))
	assert.Equal(t, "é", tail("aé", 1), "must not split a multi-byte rune")
}
