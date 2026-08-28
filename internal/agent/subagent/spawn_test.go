package subagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeProvider drives Spawn deterministically. Every Complete call
// bumps calls, sleeps for delay, then returns end_turn with the
// caller-supplied text.
type fakeProvider struct {
	name      string
	delay     time.Duration
	text      string
	calls     atomic.Int32
	failEvery int32 // fail 1 in N calls
	inFlight  atomic.Int32
	maxSeen   atomic.Int32
	tokensIn  int
	tokensOut int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Complete(ctx context.Context, _ agent.Request) (agent.Response, error) {
	f.calls.Add(1)
	in := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		old := f.maxSeen.Load()
		if in <= old || f.maxSeen.CompareAndSwap(old, in) {
			break
		}
	}
	if f.failEvery > 0 && f.calls.Load()%f.failEvery == 0 {
		return agent.Response{}, errors.New("fake failure")
	}
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return agent.Response{
		Message:    agent.NewAssistantText(f.text),
		StopReason: agent.StopEndTurn,
		Usage:      agent.Usage{InputTokens: f.tokensIn, OutputTokens: f.tokensOut},
	}, nil
}

func TestSpawn_EmptyTasksErrors(t *testing.T) {
	_, err := Spawn(context.Background(), agent.NewSession("s"), &fakeProvider{}, nil, Policy{}, nil)
	assert.ErrorIs(t, err, ErrNoTasks)
}

func TestSpawn_NilParentErrors(t *testing.T) {
	_, err := Spawn(context.Background(), nil, &fakeProvider{}, []Task{{Prompt: "x"}}, Policy{}, nil)
	assert.Error(t, err)
}

func TestSpawn_NilProviderErrors(t *testing.T) {
	_, err := Spawn(context.Background(), agent.NewSession("s"), nil, []Task{{Prompt: "x"}}, Policy{}, nil)
	assert.Error(t, err)
}

func TestSpawn_RunsEveryTaskAndPreservesOrder(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", tokensIn: 5, tokensOut: 3}
	tasks := []Task{
		{Prompt: "one"}, {Prompt: "two"}, {Prompt: "three"},
	}
	res, err := Spawn(context.Background(), agent.NewSession("s"), p, tasks, Policy{}, silentLogger())
	require.NoError(t, err)
	require.Len(t, res, 3)
	for i, r := range res {
		assert.Equal(t, i, r.TaskIndex)
		assert.NoError(t, r.Err)
		assert.Equal(t, "ok", r.FinalText)
		assert.Equal(t, 1, r.Turns)
		assert.Equal(t, 5, r.TokensIn)
		assert.Equal(t, 3, r.TokensOut)
	}
	assert.EqualValues(t, 3, p.calls.Load())
}

func TestSpawn_MaxConcurrentIsRespected(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", delay: 20 * time.Millisecond}
	tasks := make([]Task, 20)
	for i := range tasks {
		tasks[i] = Task{Prompt: "x"}
	}
	_, err := Spawn(context.Background(), agent.NewSession("s"), p, tasks,
		Policy{MaxConcurrent: 3}, silentLogger())
	require.NoError(t, err)
	assert.LessOrEqual(t, p.maxSeen.Load(), int32(3),
		"maxSeen=%d exceeded MaxConcurrent=3", p.maxSeen.Load())
}

func TestSpawn_PerTaskTimeoutCancelsSlowTask(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", delay: 200 * time.Millisecond}
	tasks := []Task{{Prompt: "x", Timeout: 10 * time.Millisecond}}
	res, err := Spawn(context.Background(), agent.NewSession("s"), p, tasks, Policy{}, silentLogger())
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.ErrorIs(t, res[0].Err, context.DeadlineExceeded)
}

func TestSpawn_ContextCancellationPropagates(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", delay: 100 * time.Millisecond}
	tasks := []Task{{Prompt: "x"}, {Prompt: "y"}, {Prompt: "z"}}
	ctx, cancel := context.WithCancel(context.Background())

	var res []Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		res, err = Spawn(ctx, agent.NewSession("s"), p, tasks, Policy{MaxConcurrent: 1}, silentLogger())
		require.NoError(t, err)
	}()
	// Let the first task start.
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()

	// At least the second and third tasks saw ctx.Canceled.
	failed := 0
	for _, r := range res {
		if errors.Is(r.Err, context.Canceled) {
			failed++
		}
	}
	assert.GreaterOrEqual(t, failed, 2)
}

func TestSpawn_BudgetShortCircuits(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", tokensIn: 40, tokensOut: 40}
	tasks := make([]Task, 5)
	for i := range tasks {
		tasks[i] = Task{Prompt: "x"}
	}
	// Budget = 150 tokens, MaxConcurrent = 1 so budget updates happen
	// sequentially. First 2 tasks (80 tokens each) pass the check;
	// task 3 sees tokensSoFar=160 >= 150 and returns ErrOverBudget.
	res, err := Spawn(context.Background(), agent.NewSession("s"), p, tasks,
		Policy{BudgetTokens: 150, MaxConcurrent: 1}, silentLogger())
	require.NoError(t, err)
	over := 0
	for _, r := range res {
		if errors.Is(r.Err, ErrOverBudget) {
			over++
		}
	}
	assert.Positive(t, over, "at least one task should have short-circuited on budget")
}

func TestSpawn_ProviderErrorSurfacesInResult(t *testing.T) {
	p := &fakeProvider{name: "fake", text: "ok", failEvery: 1}
	tasks := []Task{{Prompt: "x"}}
	res, err := Spawn(context.Background(), agent.NewSession("s"), p, tasks, Policy{}, silentLogger())
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Error(t, res[0].Err)
}

func TestSpawn_ProviderOverridePerTask(t *testing.T) {
	primary := &fakeProvider{name: "primary", text: "P"}
	override := &fakeProvider{name: "override", text: "O"}
	tasks := []Task{
		{Prompt: "a"},
		{Prompt: "b", ProviderOverride: override},
	}
	res, err := Spawn(context.Background(), agent.NewSession("s"), primary, tasks, Policy{}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "P", res[0].FinalText)
	assert.Equal(t, "O", res[1].FinalText)
	assert.EqualValues(t, 1, primary.calls.Load())
	assert.EqualValues(t, 1, override.calls.Load())
}

func TestPolicy_Defaults(t *testing.T) {
	p := Policy{}
	assert.Equal(t, 4, p.maxConcurrent())
	assert.Equal(t, 5*time.Minute, p.perTaskTimeout())
	assert.Equal(t, 32*1024, p.aggregatorMax())
}
