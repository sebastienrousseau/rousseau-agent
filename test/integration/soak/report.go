package soak

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Report renders samples + invariants as a markdown summary suitable
// for uploading as a CI artefact.
func Report(runDuration time.Duration, samples []Sample, invariants []Invariant, notes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# rousseau-agent soak report — %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Wall-clock duration: %s\n\n", runDuration.Round(time.Second))
	fmt.Fprintf(&b, "Samples captured: %d\n\n", len(samples))

	b.WriteString("## Invariants\n\n")
	for _, inv := range invariants {
		status := "✅"
		if !inv.Passed {
			status = "❌"
		}
		fmt.Fprintf(&b, "- %s **%s** — %s\n", status, inv.Name, inv.Message)
	}
	b.WriteString("\n")

	if len(notes) > 0 {
		b.WriteString("## Notes\n\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Sample series\n\n")
	b.WriteString("| Elapsed | Goroutines | Alloc | Sys | HeapInUse | FDs |\n")
	b.WriteString("|--------:|:---------:|:-----:|:---:|:---------:|:---:|\n")
	if len(samples) == 0 {
		return b.String()
	}
	origin := samples[0].At
	for _, s := range samples {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %d |\n",
			s.At.Sub(origin).Round(time.Second),
			s.Goroutines,
			humanBytes(s.AllocBytes),
			humanBytes(s.SysBytes),
			humanBytes(s.HeapInUse),
			s.FDs,
		)
	}
	return b.String()
}

// WriteReport writes the report markdown to path with mode 0644.
func WriteReport(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644) //nolint:gosec // artifact readable by the CI runner
}

// AllPassed returns true when every invariant.Passed is true. Kept
// separate from Report so the driver can decide whether to fail the
// test independently from the artefact write.
func AllPassed(invariants []Invariant) bool {
	for _, inv := range invariants {
		if !inv.Passed {
			return false
		}
	}
	return true
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}
