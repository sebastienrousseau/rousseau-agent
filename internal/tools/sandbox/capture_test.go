package sandbox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

func TestCappedBuffer_UnderCapKeepsEverything(t *testing.T) {
	b := sandbox.NewCappedBuffer(8)
	n, err := b.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, int64(0), b.Dropped())
	assert.Equal(t, "hello", b.String())
}

func TestCappedBuffer_SplitsAcrossCapAndCountsDrops(t *testing.T) {
	b := sandbox.NewCappedBuffer(8)
	for _, chunk := range []string{"hello", "world", "!!"} {
		n, err := b.Write([]byte(chunk))
		assert.NoError(t, err)
		assert.Equal(t, len(chunk), n, "Write must report the full length")
	}
	assert.Equal(t, int64(4), b.Dropped())
	assert.Equal(t, "hellowor\n[output truncated: 4 bytes dropped]", b.String())
}

func TestCappedBuffer_NonPositiveUsesDefault(t *testing.T) {
	b := sandbox.NewCappedBuffer(0)
	big := make([]byte, sandbox.DefaultOutputCap+3)
	n, err := b.Write(big)
	assert.NoError(t, err)
	assert.Equal(t, len(big), n)
	assert.Equal(t, int64(3), b.Dropped())
}
