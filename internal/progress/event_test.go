package progress

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKind_Terminal(t *testing.T) {
	tests := []struct {
		kind Kind
		want bool
	}{
		{KindTurnFinished, true},
		{KindError, true},
		{KindCancelled, true},
		{KindTurnStarted, false},
		{KindToolStarted, false},
		{KindLLMDelta, false},
		{Kind("nonsense"), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			assert.Equal(t, tc.want, tc.kind.Terminal())
		})
	}
}

func TestPublisherFunc_And_Nop(t *testing.T) {
	var got []Event
	var p Publisher = PublisherFunc(func(ev Event) { got = append(got, ev) })
	p.Publish(Event{Kind: KindThinking})
	require.Len(t, got, 1)
	assert.Equal(t, KindThinking, got[0].Kind)

	assert.NotPanics(t, func() { Nop{}.Publish(Event{}) })
}

func TestKeyContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
		want string
	}{
		{"nil context", func() context.Context { return nil }, ""},
		{"no key", context.Background, ""},
		{"with key", func() context.Context {
			return WithKey(context.Background(), "wa:123")
		}, "wa:123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, KeyFrom(tc.ctx()))
		})
	}
}

func TestEmit(t *testing.T) {
	t.Run("nil publisher is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() { Emit(context.Background(), nil, Event{Kind: KindThinking}) })
	})

	t.Run("fills key from context and stamps At", func(t *testing.T) {
		var got Event
		Emit(WithKey(context.Background(), "wa:1"),
			PublisherFunc(func(ev Event) { got = ev }),
			Event{Kind: KindToolStarted, Tool: "bash"})
		assert.Equal(t, "wa:1", got.Key)
		assert.Equal(t, "bash", got.Tool)
		assert.False(t, got.At.IsZero())
	})

	t.Run("preserves an explicit key and timestamp", func(t *testing.T) {
		at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
		var got Event
		Emit(WithKey(context.Background(), "ignored"),
			PublisherFunc(func(ev Event) { got = ev }),
			Event{Key: "explicit", At: at})
		assert.Equal(t, "explicit", got.Key)
		assert.Equal(t, at, got.At)
	})
}

func TestPublisherContext(t *testing.T) {
	assert.Nil(t, PublisherFrom(context.TODO()))
	assert.Nil(t, PublisherFrom(context.Background()))

	var got Event
	p := PublisherFunc(func(ev Event) { got = ev })
	ctx := WithPublisher(context.Background(), p)
	require.NotNil(t, PublisherFrom(ctx))
	PublisherFrom(ctx).Publish(Event{Kind: KindPaused})
	assert.Equal(t, KindPaused, got.Kind)
}
