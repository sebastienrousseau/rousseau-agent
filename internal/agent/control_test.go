package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeControl is the smallest TurnControl the tests need. Scripted
// checkpoint error + scripted drain output.
type fakeControl struct {
	checkpointErr error
	drainReturns  []string
	drainCalls    int
	checkCalls    int
}

func (f *fakeControl) Checkpoint(_ context.Context) error {
	f.checkCalls++
	return f.checkpointErr
}

func (f *fakeControl) Drain() []string {
	f.drainCalls++
	out := f.drainReturns
	f.drainReturns = nil
	return out
}

func TestWithControl_RoundTrip(t *testing.T) {
	fc := &fakeControl{}
	ctx := WithControl(context.Background(), fc)
	got := ControlFrom(ctx)
	require.NotNil(t, got)
	assert.Same(t, fc, got.(*fakeControl))
}

func TestControlFrom_NilContextReturnsNil(t *testing.T) {
	//nolint:staticcheck // intentional: exercise the nil-ctx guard, not typical usage.
	assert.Nil(t, ControlFrom(nil))
}

func TestControlFrom_BareContextReturnsNil(t *testing.T) {
	// A context that was never WithControl'd yields nil — not a
	// panic, and not a nil-typed interface that looks non-nil to a
	// naive == comparison.
	got := ControlFrom(context.Background())
	assert.Nil(t, got)
}

// Wrong-type context value: someone stashed a string under the
// control key. Type assertion in ControlFrom must fall through to
// nil, not panic.
func TestControlFrom_WrongTypeReturnsNil(t *testing.T) {
	ctx := context.WithValue(context.Background(), controlKey{}, "not a TurnControl")
	assert.Nil(t, ControlFrom(ctx))
}

func TestGate_UnsupervisedContextIsNoop(t *testing.T) {
	assert.NoError(t, gate(context.Background()))
}

func TestGate_ForwardsCheckpointResult(t *testing.T) {
	want := errors.New("cancelled")
	fc := &fakeControl{checkpointErr: want}
	ctx := WithControl(context.Background(), fc)
	assert.ErrorIs(t, gate(ctx), want)
	assert.Equal(t, 1, fc.checkCalls, "gate must call Checkpoint exactly once")
}

func TestGate_SuccessfulCheckpointReturnsNil(t *testing.T) {
	fc := &fakeControl{}
	ctx := WithControl(context.Background(), fc)
	assert.NoError(t, gate(ctx))
	assert.Equal(t, 1, fc.checkCalls)
}

func TestDrainSteered_UnsupervisedContextReturnsNil(t *testing.T) {
	assert.Nil(t, drainSteered(context.Background()))
}

func TestDrainSteered_EmptyDrainReturnsNil(t *testing.T) {
	fc := &fakeControl{drainReturns: nil}
	ctx := WithControl(context.Background(), fc)
	assert.Nil(t, drainSteered(ctx))
	assert.Equal(t, 1, fc.drainCalls)
}

func TestDrainSteered_WrapsTextsAsUserMessages(t *testing.T) {
	fc := &fakeControl{drainReturns: []string{"add tests", "then commit"}}
	ctx := WithControl(context.Background(), fc)

	msgs := drainSteered(ctx)
	require.Len(t, msgs, 2)
	assert.Equal(t, RoleUser, msgs[0].Role)
	require.Len(t, msgs[0].Content, 1)
	assert.Equal(t, ContentText, msgs[0].Content[0].Kind)
	assert.Equal(t, "add tests", msgs[0].Content[0].Text)
	assert.Equal(t, "then commit", msgs[1].Content[0].Text)
}

func TestDrainSteered_ClearsBetweenCalls(t *testing.T) {
	// Contract: Drain returns and CLEARS. A second call yields
	// nothing until new text is steered in.
	fc := &fakeControl{drainReturns: []string{"once"}}
	ctx := WithControl(context.Background(), fc)
	first := drainSteered(ctx)
	require.Len(t, first, 1)
	second := drainSteered(ctx)
	assert.Nil(t, second)
	assert.Equal(t, 2, fc.drainCalls)
}
