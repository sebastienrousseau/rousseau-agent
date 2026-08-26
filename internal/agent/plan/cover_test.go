package plan_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/plan"
)

var errStore = errors.New("checkpoint store offline")

// flakyStore delegates to an in-memory store but can be told to fail
// any of the three CheckpointStore operations, so the executor's
// persistence error paths can be observed from the outside.
type flakyStore struct {
	inner            plan.CheckpointStore
	failAppend       bool
	failList         bool
	failTruncate     bool
	appendsBeforeErr int
	appends          int
}

func newFlakyStore() *flakyStore {
	return &flakyStore{inner: plan.NewMemoryCheckpointStore()}
}

func (s *flakyStore) Append(ctx context.Context, cp plan.Checkpoint) error {
	if s.failAppend {
		if s.appends >= s.appendsBeforeErr {
			return errStore
		}
		s.appends++
	}
	return s.inner.Append(ctx, cp)
}

func (s *flakyStore) List(ctx context.Context, planID string) ([]plan.Checkpoint, error) {
	if s.failList {
		return nil, errStore
	}
	return s.inner.List(ctx, planID)
}

func (s *flakyStore) TruncateAfter(ctx context.Context, planID string, afterStep int) error {
	if s.failTruncate {
		return errStore
	}
	return s.inner.TruncateAfter(ctx, planID, afterStep)
}

func okRunner() plan.Runner {
	return plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
		return plan.StepResult{Output: "did " + s.ID}, nil
	})
}

// TestRun_StopsOnCancelledContext proves a cancelled context aborts
// before the next step runs rather than after.
func TestRun_StopsOnCancelledContext(t *testing.T) {
	var executed int
	ctx, cancel := context.WithCancel(context.Background())

	e, err := plan.New(plan.Options{
		Runner: plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
			executed++
			cancel() // the next iteration must not run
			return plan.StepResult{Output: s.ID}, nil
		}),
		Checkpoints: plan.NewMemoryCheckpointStore(),
	})
	require.NoError(t, err)

	res, err := e.Run(ctx, newPlan())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, executed)
	assert.Equal(t, 1, res.CompletedSteps)
}

func TestRun_CheckpointFailuresAbort(t *testing.T) {
	tests := []struct {
		name    string
		approve func(context.Context, plan.Step) plan.ApprovalDecision
		wantErr string
	}{
		{
			name:    "executed step",
			wantErr: "checkpoint step 0",
		},
		{
			name:    "skipped step",
			approve: func(context.Context, plan.Step) plan.ApprovalDecision { return plan.ApprovalSkip },
			wantErr: "checkpoint skip step 0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFlakyStore()
			store.failAppend = true
			e, err := plan.New(plan.Options{
				Runner:      okRunner(),
				Checkpoints: store,
				Approve:     tc.approve,
			})
			require.NoError(t, err)

			res, err := e.Run(context.Background(), newPlan())
			require.Error(t, err)
			assert.ErrorIs(t, err, errStore)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Zero(t, res.CompletedSteps)
		})
	}
}

func TestRewind_StoreFailures(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		setup func(*flakyStore)
	}{
		{"list fails on no-op rewind", 0, func(s *flakyStore) { s.failList = true }},
		{"list fails on real rewind", 2, func(s *flakyStore) { s.failList = true }},
		{"truncate fails", 1, func(s *flakyStore) { s.failTruncate = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFlakyStore()
			tc.setup(store)
			e, err := plan.New(plan.Options{Runner: okRunner(), Checkpoints: store})
			require.NoError(t, err)

			keep, err := e.Rewind(context.Background(), "p-1", tc.n)
			require.ErrorIs(t, err, errStore)
			assert.Zero(t, keep)
		})
	}
}

// TestRewind_MoreThanStoredClampsToZero proves an over-long rewind
// truncates the whole plan instead of computing a negative boundary.
func TestRewind_MoreThanStoredClampsToZero(t *testing.T) {
	store := plan.NewMemoryCheckpointStore()
	e, err := plan.New(plan.Options{Runner: okRunner(), Checkpoints: store})
	require.NoError(t, err)

	_, err = e.Run(context.Background(), newPlan())
	require.NoError(t, err)

	keep, err := e.Rewind(context.Background(), "p-1", 99)
	require.NoError(t, err)
	assert.Zero(t, keep)

	cps, err := store.List(context.Background(), "p-1")
	require.NoError(t, err)
	assert.Empty(t, cps)
}

func TestResume_ListFailurePropagates(t *testing.T) {
	store := newFlakyStore()
	store.failList = true
	e, err := plan.New(plan.Options{Runner: okRunner(), Checkpoints: store})
	require.NoError(t, err)

	res, err := e.Resume(context.Background(), newPlan())
	require.ErrorIs(t, err, errStore)
	assert.Zero(t, res.CompletedSteps)
}

// TestResume_AlreadyCompleteIsNoop proves resuming a finished plan does
// not re-run the final step.
func TestResume_AlreadyCompleteIsNoop(t *testing.T) {
	var runs int
	store := plan.NewMemoryCheckpointStore()
	e, err := plan.New(plan.Options{
		Runner: plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
			runs++
			return plan.StepResult{Output: s.ID}, nil
		}),
		Checkpoints: store,
	})
	require.NoError(t, err)

	p := newPlan()
	_, err = e.Run(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, len(p.Steps), runs)

	res, err := e.Resume(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, len(p.Steps), res.CompletedSteps)
	assert.Equal(t, len(p.Steps), runs, "no step re-runs on a completed plan")
}
