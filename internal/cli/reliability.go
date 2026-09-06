package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// newReliabilityCmd wires `rousseau reliability` — the operator-
// facing view of the four-dimension decomposition from
// arXiv:2602.16666 that Phase 2.3 introduces. Prints per-window
// scores + sub-scores + notes; --json emits a stable structure for
// scraping into Grafana / a compliance report.
//
// This command reads from an in-memory aggregator that lives on the
// daemon. Standalone invocation without a running daemon prints
// "reliability data is not yet wired to a persistent store — the
// daemon is the source of truth" and exits 0 (rather than fail —
// operators exploring the CLI should see a legible explanation).
// The persistent store lands in a follow-on commit; today the
// command's primary use is exercising the display format against
// synthetic samples and validating the API surface enterprise
// buyers will consume.
func newReliabilityCmd(opts *Options) *cobra.Command {
	var (
		jsonOut  bool
		windowS  string
		synthetic bool
	)
	cmd := &cobra.Command{
		Use:   "reliability",
		Short: "Four-dimension agent-reliability decomposition",
		Long: `Print the consistency / robustness / predictability / safety
decomposition from arXiv:2602.16666 (Rabanser et al., Feb 2026).

Each dimension has an aggregate score in [0, 1] and named
sub-scores. Overall = mean(consistency, robustness, predictability);
safety is reported separately per the paper's guidance that a rare
high-severity violation must not average out.

  rousseau reliability                  # last 7 days, human table
  rousseau reliability --window 30d     # last 30 days
  rousseau reliability --json           # machine-readable JSON
  rousseau reliability --synthetic      # dogfood output on a
                                        # canned sample set — useful
                                        # for verifying dashboard
                                        # integration before real
                                        # traffic is wired

Persistent recording lands in a follow-on commit. Today, running
without --synthetic against a fresh daemon prints a legible "no
data yet" message with a pointer to docs/reliability.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			window, err := parseReliabilityWindow(windowS)
			if err != nil {
				return err
			}
			agg := loadReliabilityAggregator(opts, synthetic)
			summary := agg.Summary(window)
			return renderReliability(cmd.OutOrStdout(), summary, jsonOut)
		},
	}
	cmd.Flags().StringVar(&windowS, "window", "7d", "rolling window: 1h, 24h, 7d, 30d (default 7d)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	cmd.Flags().BoolVar(&synthetic, "synthetic", false, "load a canned sample set for demo / dashboard-integration testing")
	return cmd
}

// parseReliabilityWindow accepts the small set of window shapes an
// operator will type without pulling in a full duration parser.
// "24h" / "48h" is delegated to time.ParseDuration; "7d" / "30d" /
// "90d" is manually expanded since Go's stdlib rejects the `d` unit.
func parseReliabilityWindow(s string) (time.Duration, error) {
	switch s {
	case "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	case "90d":
		return 90 * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --window %q: use 1h / 24h / 7d / 30d / 90d or a Go duration", s)
	}
	return d, nil
}

// loadReliabilityAggregator returns the aggregator this invocation
// should read from. Today only two paths: --synthetic loads a
// canned sample set for dashboard-integration testing, otherwise
// an empty aggregator. The wiring commit will replace the empty-
// aggregator branch with a SQLite-backed load.
func loadReliabilityAggregator(_ *Options, synthetic bool) *reliability.Aggregator {
	agg := reliability.NewAggregator(0)
	if synthetic {
		loadSyntheticReliabilitySamples(agg)
	}
	return agg
}

// loadSyntheticReliabilitySamples populates an aggregator with a
// small canned dataset exercising every dimension. Purpose: prove
// the render format + JSON schema before real traffic is wired,
// and give operators a way to see what a healthy dashboard looks
// like without waiting for a week of traffic.
func loadSyntheticReliabilitySamples(agg *reliability.Aggregator) {
	now := time.Now()
	// Consistency: stable resource CV.
	for _, tokens := range []float64{1200, 1180, 1220, 1195, 1205} {
		agg.Record(reliability.Sample{
			At: now.Add(-time.Duration(int64(tokens)) * time.Second),
			Dimension: reliability.DimConsistency,
			SubMetric: "resource_cv_tokens", Bucket: "route:/agent/turn",
			Value: tokens,
		})
	}
	// Robustness: 90% clean, 82% under fault.
	for i := 0; i < 100; i++ {
		v := 1.0
		if i%10 == 0 {
			v = 0
		}
		agg.Record(reliability.Sample{
			At: now.Add(-time.Duration(i) * time.Minute),
			Dimension: reliability.DimRobustness, SubMetric: "fault", Value: v,
			Metadata: map[string]string{"fault": "false"},
		})
	}
	for i := 0; i < 50; i++ {
		v := 1.0
		if i%6 < 1 {
			v = 0
		}
		agg.Record(reliability.Sample{
			At: now.Add(-time.Duration(i) * time.Minute),
			Dimension: reliability.DimRobustness, SubMetric: "fault", Value: v,
			Metadata: map[string]string{"fault": "true"},
		})
	}
	// Predictability: reasonable calibration.
	for _, p := range []struct{ c, y float64 }{
		{0.9, 1}, {0.85, 1}, {0.8, 1}, {0.7, 1}, {0.6, 1},
		{0.4, 0}, {0.3, 0}, {0.2, 0}, {0.15, 0}, {0.1, 0},
	} {
		agg.Record(reliability.Sample{
			Dimension: reliability.DimPredictability, SubMetric: "pair",
			Value: p.c, Metadata: map[string]string{"outcome": ternary(p.y > 0.5, "1", "0")},
		})
	}
	// Safety: 199/200 compliant, one medium violation.
	for i := 0; i < 199; i++ {
		agg.Record(reliability.Sample{
			Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1,
		})
	}
	agg.Record(reliability.Sample{Dimension: reliability.DimSafety, SubMetric: "turn", Value: 0})
	agg.Record(reliability.Sample{
		Dimension: reliability.DimSafety, SubMetric: "violation",
		Metadata: map[string]string{"severity": "medium", "constraint": "pii-detection"},
	})
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

// renderReliability writes the summary in the requested format.
// Human mode: one line per dimension + sub-scores + notes. JSON
// mode: the full Summary struct as an indented JSON document
// callers can `jq` against.
func renderReliability(w io.Writer, s reliability.Summary, jsonOut bool) error {
	if jsonOut {
		return renderReliabilityJSON(w, s)
	}
	return renderReliabilityHuman(w, s)
}

func renderReliabilityJSON(w io.Writer, s reliability.Summary) error {
	// Convert to a JSON-friendly shape — the internal Summary uses
	// time.Duration (rendered as nanoseconds) and floats-with-NaN
	// which encoding/json rejects. Rewriting keeps the on-wire
	// shape stable for callers.
	type jsonDimSum struct {
		Score       any                `json:"score"`
		SubScores   map[string]float64 `json:"sub_scores"`
		SampleCount int                `json:"sample_count"`
		Note        string             `json:"note,omitempty"`
	}
	type jsonSum struct {
		WindowSeconds  float64    `json:"window_seconds"`
		SampleCount    int        `json:"sample_count"`
		Overall        any        `json:"overall"`
		Consistency    jsonDimSum `json:"consistency"`
		Robustness     jsonDimSum `json:"robustness"`
		Predictability jsonDimSum `json:"predictability"`
		Safety         jsonDimSum `json:"safety"`
	}
	body, err := json.MarshalIndent(jsonSum{
		WindowSeconds:  s.Window.Seconds(),
		SampleCount:    s.SampleCount,
		Overall:        jsonFloat(s.Overall),
		Consistency:    convertDim(s.Consistency),
		Robustness:     convertDim(s.Robustness),
		Predictability: convertDim(s.Predictability),
		Safety:         convertDim(s.Safety),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal reliability summary: %w", err)
	}
	_, err = w.Write(append(body, '\n'))
	return err
}

func convertDim(d reliability.DimensionSummary) struct {
	Score       any                `json:"score"`
	SubScores   map[string]float64 `json:"sub_scores"`
	SampleCount int                `json:"sample_count"`
	Note        string             `json:"note,omitempty"`
} {
	return struct {
		Score       any                `json:"score"`
		SubScores   map[string]float64 `json:"sub_scores"`
		SampleCount int                `json:"sample_count"`
		Note        string             `json:"note,omitempty"`
	}{
		Score:       jsonFloat(d.Score),
		SubScores:   d.SubScores,
		SampleCount: d.SampleCount,
		Note:        d.Note,
	}
}

// jsonFloat renders a NaN as JSON null (encoding/json's default is
// to error out) while passing through finite floats verbatim.
func jsonFloat(v float64) any {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return v
}

func renderReliabilityHuman(w io.Writer, s reliability.Summary) error {
	fmt.Fprintf(w, "Reliability over %s (%d samples)\n\n", //nolint:errcheck // CLI output
		humanWindow(s.Window), s.SampleCount)

	if s.SampleCount == 0 {
		fmt.Fprintln(w, "(no samples recorded yet — see docs/reliability.md")   //nolint:errcheck
		fmt.Fprintln(w, " for methodology + wiring status. Try --synthetic for") //nolint:errcheck
		fmt.Fprintln(w, " a canned example so you can verify the format.)")     //nolint:errcheck
		return nil
	}

	fmt.Fprintf(w, "  Overall   %s   (mean of consistency, robustness, predictability)\n", humanScore(s.Overall)) //nolint:errcheck
	fmt.Fprintln(w)                                                                                                //nolint:errcheck
	renderDim(w, s.Consistency)
	renderDim(w, s.Robustness)
	renderDim(w, s.Predictability)
	// Safety printed with the "reported separately" marker so
	// nobody reads it as part of the Overall.
	fmt.Fprintln(w, "  ──── Safety (reported separately; never folded into Overall) ────") //nolint:errcheck
	renderDim(w, s.Safety)
	return nil
}

func renderDim(w io.Writer, d reliability.DimensionSummary) {
	fmt.Fprintf(w, "  %-14s %s   (%d samples)\n", d.Dimension, humanScore(d.Score), d.SampleCount) //nolint:errcheck
	for _, k := range sortedKeys(d.SubScores) {
		fmt.Fprintf(w, "      %-30s %s\n", k, humanScore(d.SubScores[k])) //nolint:errcheck
	}
	if d.Note != "" {
		fmt.Fprintf(w, "      note: %s\n", d.Note) //nolint:errcheck
	}
	fmt.Fprintln(w) //nolint:errcheck
}

// sortedKeys is a stable-render helper so the human output orders
// sub-scores deterministically. Uses an inline sort to avoid the
// dependency on the `sort` package outside the paths that need it.
func sortedKeys(m map[string]float64) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — small maps (≤5 sub-scores per dim).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// humanScore formats a [0,1] score as a percentage with one
// decimal, or "n/a" when NaN.
func humanScore(v float64) string {
	if math.IsNaN(v) {
		return "  n/a"
	}
	return fmt.Sprintf("%5.1f%%", v*100)
}

// humanWindow renders "7d" / "24h" etc. rather than the Go stdlib's
// "168h0m0s" default which is ugly for operator eyes.
func humanWindow(d time.Duration) string {
	days := d / (24 * time.Hour)
	if days > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(days))
	}
	return d.String()
}
