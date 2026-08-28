package audio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// errBackend returns a canned error on every Transcribe.
type errBackend struct{}

func (*errBackend) Kind() string { return "err" }
func (*errBackend) Transcribe(_ context.Context, _ []byte, _ string) (audio.Result, error) {
	return audio.Result{}, errors.New("boom")
}

func TestTranscriberString_NilBackendReturnsNil(t *testing.T) {
	assert.Nil(t, audio.NewTranscriberString(nil))
}

func TestTranscriberString_ForwardsToBackend(t *testing.T) {
	adapter := audio.NewTranscriberString(&audio.Noop{Text: "hello world"})
	require.NotNil(t, adapter)
	got, err := adapter.Transcribe(context.Background(), []byte("ignored"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "hello world", got)
}

func TestTranscriberString_PropagatesBackendError(t *testing.T) {
	adapter := audio.NewTranscriberString(&errBackend{})
	_, err := adapter.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	assert.Error(t, err)
}
