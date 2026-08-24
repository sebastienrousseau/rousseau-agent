package plan_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/plan"
)

func newPlan() plan.Plan {
	return plan.Plan{
		ID:   "p-1",
		Goal: "test",
		Steps: []plan.Step{
			{ID: "s-1", Description: "one"},
			{ID: "s-2", Description: "two"},
			{ID: "s-3", Description: "three"},
		},
	}
}

func TestNew_ValidatesRunnerAndStore(t *testing.T) {
	_, err := plan.New(plan.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Runner")

	_, err = plan.New(plan.Options{Runner: plan.RunnerFunc(func(_ context.Context, _ plan.Step) (plan.StepResult, error) {
		return plan.StepResult{}, nil
	})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Checkpoints")
}

func TestExecutor_Run_HappyPath(t *testing.T) {
	var executed []string
	runner := plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
		executed = append(executed, s.ID)
		return plan.StepResult{Output: "ok-" + s.ID}, nil
	})
	cps := plan.NewMemoryCheckpointStore()
	e, err := plan.New(plan.Options{Runner: runner, Checkpoints: cps})
	require.NoError(t, err)

	res, err := e.Run(context.Background(), newPlan())
	require.NoError(t, err)
	assert.Equal(t, 3, res.CompletedSteps)
	assert.Equal(t, []string{"s-1", "s-2", "s-3"}, executed)
	assert.Equal(t, "ok-s-3", res.LastOutput)

	stored, err := cps.List(context.Background(), "p-1")
	require.NoError(t, err)
	require.Len(t, stored, 3)
	for i, cp := range stored {
		assert.Equal(t, i, cp.AtStep)
	}
}

func TestExecutor_Approve_Reject_StopsExecution(t *testing.T) {
	runner := plan.RunnerFunc(func(_ context.Context, _ plan.Step) (plan.StepResult, error) {
		t.Fatal("runner should not have been invoked")
		return plan.StepResult{}, nil
	})
	e, err := plan.New(plan.Options{
		Runner:      runner,
		Checkpoints: plan.NewMemoryCheckpointStore(),
		Approve: func(_ context.Context, _ plan.Step) plan.ApprovalDecision {
			return plan.ApprovalReject
		},
	})
	require.NoError(t, err)
	res, err := e.Run(context.Background(), newPlan())
	require.ErrorIs(t, err, plan.ErrRejected)
	assert.True(t, res.Rejected)
	assert.Equal(t, 0, res.RejectedAtStep)
}

func TestExecutor_Approve_Skip_RecordsSyntheticCheckpoint(t *testing.T) {
	var executed int
	runner := plan.RunnerFunc(func(_ context.Context, _ plan.Step) (plan.StepResult, error) {
		executed++
		return plan.StepResult{Output: "ran"}, nil
	})
	cps := plan.NewMemoryCheckpointStore()

	// Skip the first step, approve the rest.
	call := 0
	e, err := plan.New(plan.Options{
		Runner:      runner,
		Checkpoints: cps,
		Approve: func(_ context.Context, _ plan.Step) plan.ApprovalDecision {
			call++
			if call == 1 {
				return plan.ApprovalSkip
			}
			return plan.ApprovalApprove
		},
	})
	require.NoError(t, err)
	res, err := e.Run(context.Background(), newPlan())
	require.NoError(t, err)
	assert.Equal(t, 3, res.CompletedSteps)
	assert.Equal(t, 2, executed, "runner should have run 2 of 3 steps")

	stored, err := cps.List(context.Background(), "p-1")
	require.NoError(t, err)
	require.Len(t, stored, 3)
	assert.Equal(t, "(skipped by approval gate)", stored[0].Output)
}

func TestExecutor_Rewind_TruncatesCheckpoints(t *testing.T) {
	runner := plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
		return plan.StepResult{Output: "ok-" + s.ID}, nil
	})
	cps := plan.NewMemoryCheckpointStore()
	e, err := plan.New(plan.Options{Runner: runner, Checkpoints: cps})
	require.NoError(t, err)
	_, err = e.Run(context.Background(), newPlan())
	require.NoError(t, err)

	remaining, err := e.Rewind(context.Background(), "p-1", 2)
	require.NoError(t, err)
	assert.Equal(t, 1, remaining, "rewinding 2 of 3 leaves 1")

	after, err := cps.List(context.Background(), "p-1")
	require.NoError(t, err)
	assert.Len(t, after, 1)
	assert.Equal(t, "s-1", after[0].StepID)
}

func TestExecutor_Rewind_ZeroReturnsCurrentCount(t *testing.T) {
	runner := plan.RunnerFunc(func(_ context.Context, _ plan.Step) (plan.StepResult, error) {
		return plan.StepResult{}, nil
	})
	cps := plan.NewMemoryCheckpointStore()
	e, err := plan.New(plan.Options{Runner: runner, Checkpoints: cps})
	require.NoError(t, err)
	_, err = e.Run(context.Background(), newPlan())
	require.NoError(t, err)
	n, err := e.Rewind(context.Background(), "p-1", 0)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestExecutor_Resume_PicksUpFromLastCheckpoint(t *testing.T) {
	// Manually seed a checkpoint for s-1, then Resume — only s-2 and
	// s-3 should run.
	cps := plan.NewMemoryCheckpointStore()
	require.NoError(t, cps.Append(context.Background(), plan.Checkpoint{
		PlanID: "p-1", StepID: "s-1", AtStep: 0, Output: "prior",
	}))

	var executed []string
	runner := plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
		executed = append(executed, s.ID)
		return plan.StepResult{Output: "ok-" + s.ID}, nil
	})
	e, err := plan.New(plan.Options{Runner: runner, Checkpoints: cps})
	require.NoError(t, err)
	res, err := e.Resume(context.Background(), newPlan())
	require.NoError(t, err)
	assert.Equal(t, []string{"s-2", "s-3"}, executed)
	assert.Equal(t, 3, res.CompletedSteps)
}

func TestExecutor_Run_PropagatesRunnerError(t *testing.T) {
	runner := plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
		if s.ID == "s-2" {
			return plan.StepResult{}, errors.New("boom")
		}
		return plan.StepResult{Output: "ok"}, nil
	})
	e, err := plan.New(plan.Options{Runner: runner, Checkpoints: plan.NewMemoryCheckpointStore()})
	require.NoError(t, err)
	res, err := e.Run(context.Background(), newPlan())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.Equal(t, 1, res.CompletedSteps, "only step 0 recorded before failure")
}

func TestOnStepComplete_Fires(t *testing.T) {
	var got []string
	e, err := plan.New(plan.Options{
		Runner: plan.RunnerFunc(func(_ context.Context, s plan.Step) (plan.StepResult, error) {
			return plan.StepResult{Output: "r-" + s.ID}, nil
		}),
		Checkpoints: plan.NewMemoryCheckpointStore(),
		OnStepComplete: func(_ context.Context, s plan.Step, _ plan.StepResult) {
			got = append(got, s.ID)
		},
	})
	require.NoError(t, err)
	_, err = e.Run(context.Background(), newPlan())
	require.NoError(t, err)
	assert.Equal(t, []string{"s-1", "s-2", "s-3"}, got)
}

func TestMemoryCheckpointStore_AppendRequiresPlanID(t *testing.T) {
	cps := plan.NewMemoryCheckpointStore()
	err := cps.Append(context.Background(), plan.Checkpoint{})
	require.Error(t, err)
}
