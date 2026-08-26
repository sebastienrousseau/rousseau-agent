package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReturnsTopHits(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	require.Equal(t, 0, code, errOut.String())

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2, "retriever was asked for top-2")

	// Every hit is one of the ingested rows, printed as
	// "[score] text" with scores in descending order.
	corpus := []string{
		"the whatsapp transport fires QR pairing on first launch",
		"slack socket mode uses xapp- + xoxb- token pair",
		"signal transport shells out to signal-cli in JSON-RPC mode",
		"matrix homeserver URL + access token wire into the room stream",
	}
	prev := math.Inf(1)
	for _, line := range lines {
		require.Regexp(t, `^\[\d+\.\d{3}\] .+$`, line)
		var score float64
		var text string
		_, err := fmt.Sscanf(line, "[%f] ", &score)
		require.NoError(t, err)
		text = line[strings.Index(line, "] ")+2:]
		assert.Contains(t, corpus, text)
		assert.LessOrEqual(t, score, prev, "hits must be ordered by descending score")
		prev = score
	}
	assert.Empty(t, errOut.String())
}

func TestRunReportsStoreFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer

	code := run(ctx, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-recall:")
	assert.Contains(t, errOut.String(), "context canceled")
}
