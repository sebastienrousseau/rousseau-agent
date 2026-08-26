package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLinksHandlesToOneIdentity(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	got := out.String()

	assert.Contains(t, got, "provisioned identity for whatsapp:+447906009073 →")
	assert.Contains(t, got, "linked slack:U01234 to Alice's identity")
	// The whole point of the example: both transports collapse onto
	// one identity ID.
	assert.Contains(t, got, "same identity: true")
	assert.Contains(t, got, "  Display: Alice")
	assert.Contains(t, got, "  Handles: 2")
	assert.Contains(t, got, "    whatsapp:+447906009073")
	assert.Contains(t, got, "    slack:U01234")
	assert.Empty(t, errOut.String())
}

func TestRunReportsStoreFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer

	code := run(ctx, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-identity:")
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

func TestRunReportsMidFlightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelAfter{marker: "provisioned identity", cancel: cancel}
	var errOut bytes.Buffer

	// Cancelling once the identity exists but before /link lands must
	// surface as a failure rather than a silently half-linked user.
	code := run(ctx, out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-identity: link:")
	assert.Contains(t, errOut.String(), "context canceled")
	assert.NotContains(t, out.buf.String(), "linked slack:U01234")
}
