package progress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{500 * time.Millisecond, "0s"},
		{8 * time.Second, "8s"},
		{72 * time.Second, "1m12s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{2*time.Hour + 3*time.Minute, "2h03m"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, FormatDuration(tc.in))
		})
	}
}

func TestRender_LiveHeadlines(t *testing.T) {
	tests := []struct {
		name string
		st   State
		want string
	}{
		{"idle", State{}, "working on it"},
		{"thinking on a later round", State{Iteration: 3}, "thinking (round 3)"},
		{"first round is not numbered", State{Iteration: 1}, "working on it"},
		{"one tool", State{Running: []string{"bash"}}, "running `bash`"},
		{"several tools", State{Running: []string{"bash", "read"}}, "running `bash`, `read`"},
		{"sub-agents", State{SubagentsRunning: 3}, "3 sub-agents working"},
		{"writing", State{Preview: "the answer so far"}, "writing the answer"},
		{"paused wins over everything", State{Paused: true, Running: []string{"bash"}}, "paused — send /resume to continue"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.st, 30*time.Second)
			assert.Contains(t, got, "⏳ 30s · "+tc.want)
		})
	}
}

func TestRender_StatsLine(t *testing.T) {
	st := State{
		Cron:             "nightly",
		Step:             2,
		Of:               5,
		ToolsDone:        9,
		ToolsFailed:      1,
		ToolsDenied:      2,
		SubagentsRunning: 1,
		SubagentsDone:    1,
		Steers:           1,
		Dropped:          4,
		Running:          []string{"bash"},
	}
	got := Render(st, 90*time.Second)
	assert.Contains(t, got, "cron: nightly")
	assert.Contains(t, got, "step 2/5")
	assert.Contains(t, got, "9 tools")
	assert.Contains(t, got, "1 failed")
	assert.Contains(t, got, "2 blocked")
	assert.Contains(t, got, "2 sub-agents")
	assert.Contains(t, got, "1 note from you")
	assert.Contains(t, got, "…")
}

func TestRender_SingularAndPluralTallies(t *testing.T) {
	got := Render(State{ToolsDone: 1, SubagentsDone: 1, Steers: 2}, time.Minute)
	assert.Contains(t, got, "1 tool ·")
	assert.Contains(t, got, "1 sub-agent")
	assert.Contains(t, got, "2 notes from you")
}

func TestRender_PreviewIsCollapsedToOneLine(t *testing.T) {
	got := Render(State{Preview: "line one\n\n  line two "}, 10*time.Second)
	assert.Contains(t, got, "… line one line two")
}

func TestRender_NoStatsLineWhenNothingToReport(t *testing.T) {
	assert.Equal(t, "⏳ 10s · working on it", Render(State{}, 10*time.Second))
}

func TestRender_Terminal(t *testing.T) {
	tests := []struct {
		name string
		st   State
		want string
	}{
		{
			"success with tally",
			State{Terminal: true, Outcome: KindTurnFinished, ToolsDone: 9},
			"✅ done in 4m12s · 9 tools",
		},
		{
			"success with nothing to tally",
			State{Terminal: true, Outcome: KindTurnFinished},
			"✅ done in 4m12s",
		},
		{
			"cancelled",
			State{Terminal: true, Outcome: KindCancelled, ToolsDone: 3},
			"⏹️ cancelled after 4m12s · 3 tools",
		},
		{
			"failed with a reason",
			State{Terminal: true, Outcome: KindError, Err: "provider:\n timeout"},
			"⚠️ failed after 4m12s — provider: timeout",
		},
		{
			"failed without a reason",
			State{Terminal: true, Outcome: KindError},
			"⚠️ failed after 4m12s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Render(tc.st, 4*time.Minute+12*time.Second))
		})
	}
}
