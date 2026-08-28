package progress

import (
	"strings"
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
			assert.Contains(t, got, GlyphWorking+" 30s · "+tc.want)
		})
	}
}

func TestRender_LiveNoBulletsIsSpinnerOnly(t *testing.T) {
	assert.Equal(t, GlyphWorking+" 10s · working on it", Render(State{}, 10*time.Second))
}

func TestRender_LiveWithBulletsDrawsFeed(t *testing.T) {
	st := State{
		Bullets: []Bullet{
			{Text: "Read foo.go"},
			{Text: "Bash go test", Failed: true},
			{Text: "Write bar.go", Denied: true},
		},
		Running: []string{"grep"},
	}
	got := Render(st, 30*time.Second)
	want := strings.Join([]string{
		GlyphBullet + " Read foo.go",
		GlyphFailed + " Bash go test",
		GlyphDenied + " Write bar.go",
		GlyphWorking + " 30s · running `grep`",
	}, "\n")
	assert.Equal(t, want, got)
}

func TestRender_LiveMetaLineHasNonToolContextOnly(t *testing.T) {
	// The bullets above already say "9 tools done" — the meta line
	// deliberately does NOT repeat tool counts to avoid noise.
	st := State{
		Cron:      "nightly",
		Step:      2,
		Of:        5,
		ToolsDone: 9, // MUST NOT appear on the meta line
		Steers:    2,
		Dropped:   4,
		Running:   []string{"bash"},
	}
	got := Render(st, 90*time.Second)
	assert.Contains(t, got, "cron: nightly · step 2/5 · 2 notes from you · 4 events dropped")
	// The live line must not carry the "9 tools" rollup — that lives
	// on terminal lines where the bullet log may have been trimmed.
	assert.NotContains(t, got, "9 tools")
}

func TestRender_LivePreviewLineTrailsBelow(t *testing.T) {
	got := Render(State{Preview: "line one\n\n  line two "}, 10*time.Second)
	assert.Contains(t, got, "\n"+GlyphPreview+" line one line two")
}

func TestRender_LiveTrimmedBulletsAreSignalled(t *testing.T) {
	st := State{
		Bullets:        []Bullet{{Text: "Grep"}},
		bulletsTrimmed: true,
	}
	got := Render(st, 30*time.Second)
	assert.True(t, strings.HasPrefix(got, GlyphTrim+" (earlier steps trimmed)\n"+GlyphBullet+" Grep\n"))
}

func TestRender_Terminal(t *testing.T) {
	tests := []struct {
		name string
		st   State
		want string
	}{
		{
			"success with tally and no prior bullets",
			State{Terminal: true, Outcome: KindTurnFinished, ToolsDone: 9},
			GlyphBullet + " done in 4m12s · 9 tools",
		},
		{
			"success with nothing to tally",
			State{Terminal: true, Outcome: KindTurnFinished},
			GlyphBullet + " done in 4m12s",
		},
		{
			"cancelled",
			State{Terminal: true, Outcome: KindCancelled, ToolsDone: 3},
			GlyphBullet + " stopped after 4m12s · 3 tools",
		},
		{
			"failed with a reason",
			State{Terminal: true, Outcome: KindError, Err: "provider:\n timeout"},
			GlyphFailed + " failed after 4m12s — provider: timeout",
		},
		{
			"failed without a reason",
			State{Terminal: true, Outcome: KindError},
			GlyphFailed + " failed after 4m12s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Render(tc.st, 4*time.Minute+12*time.Second))
		})
	}
}

func TestRender_TerminalWithBulletsDrawsWholeFeed(t *testing.T) {
	st := State{
		Terminal:  true,
		Outcome:   KindTurnFinished,
		ToolsDone: 2,
		Bullets: []Bullet{
			{Text: "Read foo.go"},
			{Text: "Bash go test"},
		},
	}
	want := strings.Join([]string{
		GlyphBullet + " Read foo.go",
		GlyphBullet + " Bash go test",
		GlyphBullet + " done in 42s · 2 tools",
	}, "\n")
	assert.Equal(t, want, Render(st, 42*time.Second))
}

func TestRender_TerminalSuffixIncludesEveryTally(t *testing.T) {
	// countParts is the source of truth for the "· N tools · M failed
	// · … · P sub-agents" rollup on the closing line.
	st := State{
		Terminal:         true,
		Outcome:          KindTurnFinished,
		ToolsDone:        5,
		ToolsFailed:      2,
		ToolsDenied:      1,
		SubagentsRunning: 1,
		SubagentsDone:    1,
	}
	got := Render(st, 30*time.Second)
	assert.Contains(t, got, "5 tools")
	assert.Contains(t, got, "2 failed")
	assert.Contains(t, got, "1 blocked")
	assert.Contains(t, got, "2 sub-agents")
}

func TestRender_TerminalDoesNotDrawPreview(t *testing.T) {
	// The preview belongs on live updates only — on the terminal line
	// the assistant's final text will follow as its own message.
	got := Render(State{Terminal: true, Outcome: KindTurnFinished, Preview: "half-written"}, 5*time.Second)
	assert.NotContains(t, got, "half-written")
}

func TestBulletGlyph(t *testing.T) {
	assert.Equal(t, GlyphBullet, bulletGlyph(Bullet{}))
	assert.Equal(t, GlyphFailed, bulletGlyph(Bullet{Failed: true}))
	assert.Equal(t, GlyphDenied, bulletGlyph(Bullet{Denied: true}))
	assert.Equal(t, GlyphFailed, bulletGlyph(Bullet{Failed: true, Denied: true}),
		"failure ranks above denial when both flags are set")
}
