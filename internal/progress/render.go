package progress

import (
	"fmt"
	"strings"
	"time"
)

// Glyphs used by the renderer. Kept as named constants so the styling
// is trivially auditable in one place. Every glyph is a single Unicode
// codepoint that renders as a clean monochrome mark on WhatsApp,
// iMessage, Signal, Telegram, terminal — no emoji variation selectors,
// no colour, no line-height surprises. The set intentionally mirrors
// what the Claude CLI draws in the terminal so the multi-transport UX
// feels like one product.
const (
	// GlyphBullet marks a completed action.
	GlyphBullet = "●"
	// GlyphFailed marks an action that returned an error.
	GlyphFailed = "✗"
	// GlyphDenied marks an action that was blocked by an approver or hook.
	GlyphDenied = "⊘"
	// GlyphWorking is the "spinner" mark on the live-status line.
	GlyphWorking = "✻"
	// GlyphPreview leads a streamed-text preview line.
	GlyphPreview = "⎿"
	// GlyphTrim leads the marker bullet drawn when older bullets were
	// dropped to keep the log bounded.
	GlyphTrim = "…"
)

// Render turns a coalesced State into the message body a chat client
// will show. The layout mirrors the Claude CLI's terminal feed:
//
//	● Read foo.go
//	● Bash go test
//	● Editing bar.go
//	✻ Working… 30s · running `find`
//	⎿ the streamed answer so far
//
// Terminal turns swap the spinner for a summary bullet:
//
//	● Read foo.go
//	● Bash go test
//	● done in 42s · 3 tools
//
// Rendering is a pure function of (State, elapsed) so tests assert on
// exact text without constructing a transport.
func Render(st State, elapsed time.Duration) string {
	var b strings.Builder
	writeBullets(&b, st)
	if st.Terminal {
		writeTerminalLine(&b, st, elapsed)
	} else {
		writeLiveLine(&b, st, elapsed)
	}
	if meta := renderMeta(st); meta != "" {
		b.WriteString("\n")
		b.WriteString(meta)
	}
	if !st.Terminal {
		if preview := oneLine(st.Preview); preview != "" {
			b.WriteString("\n")
			b.WriteString(GlyphPreview)
			b.WriteString(" ")
			b.WriteString(preview)
		}
	}
	return b.String()
}

// writeBullets writes each accumulated bullet on its own line, followed
// by a trailing newline separator so the live/terminal line lands on a
// fresh line. Emits nothing (not even a newline) when the log is empty.
func writeBullets(b *strings.Builder, st State) {
	if st.bulletsTrimmed {
		b.WriteString(GlyphTrim)
		b.WriteString(" (earlier steps trimmed)\n")
	}
	for _, bl := range st.Bullets {
		b.WriteString(bulletGlyph(bl))
		b.WriteString(" ")
		b.WriteString(bl.Text)
		b.WriteString("\n")
	}
}

// RenderDelta is the sequential-mode renderer. Instead of the full
// cumulative view Render produces, it emits ONLY the bullets that
// arrived since the previous emit (State.Bullets[fromIdx:]) — the
// message body for one delta-mode Update.
//
// Two exit paths:
//
//   Terminal: draws the closing summary line ("● done in Ns · N tools",
//             "✗ failed after …", "● stopped after …"). Any bullets
//             that landed since the previous emit precede it on their
//             own lines so a burst-then-done still shows what ran.
//
//   Non-terminal: just the new bullets, one per line. No spinner —
//                 a delta with zero new bullets never reaches this
//                 function (Coalescer.readySequential gates it), and
//                 a spinner line would be redundant noise in a
//                 chronological feed.
//
// Trimmed-bullet marker: the "… (earlier steps trimmed)" line only
// appears on the FIRST delta after a trim, so the reader sees the
// signal once instead of on every subsequent emit.
func RenderDelta(st State, fromIdx int, elapsed time.Duration, terminal bool) string {
	if fromIdx < 0 {
		fromIdx = 0
	}
	if fromIdx > len(st.Bullets) {
		fromIdx = len(st.Bullets)
	}
	var b strings.Builder
	if st.bulletsTrimmed && fromIdx == 0 {
		b.WriteString(GlyphTrim)
		b.WriteString(" (earlier steps trimmed)\n")
	}
	for _, bl := range st.Bullets[fromIdx:] {
		b.WriteString(bulletGlyph(bl))
		b.WriteString(" ")
		b.WriteString(bl.Text)
		b.WriteString("\n")
	}
	if terminal {
		writeTerminalLine(&b, st, elapsed)
		return b.String()
	}
	return strings.TrimRight(b.String(), "\n")
}

// bulletGlyph picks the mark for one bullet.
func bulletGlyph(b Bullet) string {
	switch {
	case b.Failed:
		return GlyphFailed
	case b.Denied:
		return GlyphDenied
	default:
		return GlyphBullet
	}
}

// writeLiveLine writes the spinner line for a non-terminal state.
func writeLiveLine(b *strings.Builder, st State, elapsed time.Duration) {
	fmt.Fprintf(b, "%s %s · %s", GlyphWorking, FormatDuration(elapsed), headline(st))
}

// writeTerminalLine writes the closing bullet — same glyph as a done
// tool, so the whole feed reads as one uninterrupted list of actions
// ending with "done in …".
func writeTerminalLine(b *strings.Builder, st State, elapsed time.Duration) {
	switch st.Outcome {
	case KindCancelled:
		fmt.Fprintf(b, "%s stopped after %s%s", GlyphBullet, FormatDuration(elapsed), suffix(st))
	case KindError:
		fmt.Fprintf(b, "%s failed after %s%s", GlyphFailed, FormatDuration(elapsed), suffix(st))
		if st.Err != "" {
			b.WriteString(" — ")
			b.WriteString(oneLine(st.Err))
		}
	default:
		fmt.Fprintf(b, "%s done in %s%s", GlyphBullet, FormatDuration(elapsed), suffix(st))
	}
}

// suffix appends the tool/sub-agent tally to a terminal line, or
// nothing when the turn used neither. The bullets above the summary
// already list each tool; the tally is a rollup that also captures
// entries that fell off the front of the bullet log.
func suffix(st State) string {
	parts := countParts(st)
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// headline is the single phrase describing what the turn is doing
// right now.
func headline(st State) string {
	switch {
	case st.Paused:
		return "paused — send /resume to continue"
	case len(st.Running) == 1:
		return "running `" + st.Running[0] + "`"
	case len(st.Running) > 1:
		return "running `" + strings.Join(st.Running, "`, `") + "`"
	case st.SubagentsRunning > 0:
		return fmt.Sprintf("%d sub-agents working", st.SubagentsRunning)
	case st.Preview != "":
		return "writing the answer"
	case st.Iteration > 1:
		return fmt.Sprintf("thinking (round %d)", st.Iteration)
	default:
		return "working on it"
	}
}

// renderMeta is the optional line under the spinner: metadata that
// isn't visible from the bullet feed (cron, plan cursor, mid-flight
// steers, dropped-event notice). The per-tool tallies live in the
// suffix() of terminal lines only — repeating them in live output
// would just duplicate the bullets above.
func renderMeta(st State) string {
	parts := make([]string, 0, 4)
	if st.Cron != "" {
		parts = append(parts, "cron: "+st.Cron)
	}
	if st.Of > 0 {
		parts = append(parts, fmt.Sprintf("step %d/%d", st.Step, st.Of))
	}
	if st.Steers > 0 {
		parts = append(parts, fmt.Sprintf("%d note%s from you", st.Steers, plural(st.Steers)))
	}
	if st.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d event%s dropped", st.Dropped, plural(st.Dropped)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// countParts renders the tool + sub-agent tallies used by suffix() on
// terminal lines. Kept separate from renderMeta because live output
// deliberately does NOT repeat the per-tool counts (the bullets say it).
func countParts(st State) []string {
	var parts []string
	if st.ToolsDone > 0 {
		parts = append(parts, fmt.Sprintf("%d tool%s", st.ToolsDone, plural(st.ToolsDone)))
	}
	if st.ToolsFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", st.ToolsFailed))
	}
	if st.ToolsDenied > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", st.ToolsDenied))
	}
	if total := st.SubagentsRunning + st.SubagentsDone; total > 0 {
		parts = append(parts, fmt.Sprintf("%d sub-agent%s", total, plural(total)))
	}
	return parts
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// oneLine collapses whitespace so a preview never breaks the layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// FormatDuration renders d as a compact human string: "8s", "1m12s",
// "2h03m". Sub-second and negative durations render as "0s".
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	total := int(d / time.Second)
	h, m, s := total/3600, (total/60)%60, total%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
