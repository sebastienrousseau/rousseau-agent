package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRecordsAndReportsCosts(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	got := out.String()

	// Two completions land on s1, one on s2, and the tokens are the
	// sum of what was recorded.
	assert.Contains(t, got, "s1: 2 completions, in=5200 out=1100 cache-r=24000")
	assert.Contains(t, got, "s2: 1 completions, in=1200 out=5000 cache-r=0")
	assert.Contains(t, got, "top by cost:")
	// Opus output tokens dominate, so s2 is the most expensive session.
	assert.Regexp(t, `1\. s2: \$\d+\.\d{4} \(1 completions\)`, got)
	assert.Regexp(t, `2\. s1: \$\d+\.\d{4} \(2 completions\)`, got)
	assert.Empty(t, errOut.String())
}

func TestRunReportsStoreFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer

	code := run(ctx, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-cost:")
	assert.Contains(t, errOut.String(), "context canceled")
}

// cancelAfter is an io.Writer that cancels a context as soon as the
// marker line is written, letting a test fail the demo at a chosen
// point rather than only at the first statement.
type cancelAfter struct {
	buf    bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (w *cancelAfter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if strings.Contains(string(p), w.marker) {
		w.cancel()
	}
	return n, err
}

func TestRunReportsSummaryFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelAfter{marker: "s1: ", cancel: cancel}
	var errOut bytes.Buffer

	// Cancelling between the two per-session summaries must abort the
	// report rather than print a partial one as if it were complete.
	code := run(ctx, out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-cost: sum s2:")
	assert.NotContains(t, out.buf.String(), "top by cost")
}

func TestRunReportsTopSessionsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelAfter{marker: "s2: ", cancel: cancel}
	var errOut bytes.Buffer

	code := run(ctx, out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-cost: top sessions:")
}
