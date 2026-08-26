package progress

import (
	"fmt"
	"strings"
	"time"
)

// Render turns a coalesced State into the message body a chat client
// will show. Output is deliberately compact — three short lines at
// most — because it may be edited in place a dozen times and sits in
// the same thread as real answers.
//
// Rendering is a pure function of (State, elapsed) so a test can
// assert the exact text without constructing a transport.
func Render(st State, elapsed time.Duration) string {
	if st.Terminal {
		return renderTerminal(st, elapsed)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "⏳ %s · %s", FormatDuration(elapsed), headline(st))
	if stats := renderStats(st); stats != "" {
		b.WriteString("\n" + stats)
	}
	if preview := oneLine(st.Preview); preview != "" {
		b.WriteString("\n… " + preview)
	}
	return b.String()
}

// renderTerminal renders the closing update of a turn.
func renderTerminal(st State, elapsed time.Duration) string {
	switch st.Outcome {
	case KindCancelled:
		return "⏹️ cancelled after " + FormatDuration(elapsed) + suffix(st)
	case KindError:
		msg := "⚠️ failed after " + FormatDuration(elapsed) + suffix(st)
		if st.Err != "" {
			msg += " — " + oneLine(st.Err)
		}
		return msg
	default:
		return "✅ done in " + FormatDuration(elapsed) + suffix(st)
	}
}

// suffix appends the tool/sub-agent tally to a terminal line, or
// nothing when the turn used neither.
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

// renderStats is the second line: the running tallies.
func renderStats(st State) string {
	parts := make([]string, 0, 6)
	if st.Cron != "" {
		parts = append(parts, "cron: "+st.Cron)
	}
	if st.Of > 0 {
		parts = append(parts, fmt.Sprintf("step %d/%d", st.Step, st.Of))
	}
	parts = append(parts, countParts(st)...)
	if st.Steers > 0 {
		parts = append(parts, fmt.Sprintf("%d note%s from you", st.Steers, plural(st.Steers)))
	}
	if st.Dropped > 0 {
		parts = append(parts, "…")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// countParts renders the tool + sub-agent tallies shared by the live
// and terminal renderers.
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
