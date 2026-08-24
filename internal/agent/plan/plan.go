// Package plan implements a checkpointed multi-step agent plan on top
// of the linear [agent.Turn] loop. Matches the "planning phase before
// execution" idiom Aider (architect mode), Cline (plan mode), and
// OpenCode (plan agent) all ship.
//
// A Plan is an ordered set of Steps produced by an initial LLM call,
// then executed one step at a time with optional per-step approval
// gates. Every step boundary is a checkpoint: `Rewind(n)` truncates
// state to the checkpoint n steps back and resumes from there.
//
// # Status
//
// Runtime shipped in `v0.0.2`:
//
//   - `Executor` walks a Plan step-by-step, records a `Checkpoint`
//     per step, and drops approval gates + failure hooks through
//     `Options`.
//   - `MemoryCheckpointStore` gives operators a working store out of
//     the box; a SQLite-backed store is a follow-up that plugs behind
//     the same [CheckpointStore] interface.
//   - `New` accepts a [Runner] (the seam that runs one step) and
//     returns an `Executor`. Full DAG (non-sequential) plans and the
//     `/plan` chat command land in a follow-up.
package plan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Plan is the sequenced set of steps to execute.
type Plan struct {
	// ID is a UUID. Persisted alongside checkpoints.
	ID string
	// Goal is the user-authored request that produced this plan.
	Goal string
	// Steps are executed in slice order.
	Steps []Step
	// CreatedAt is when the plan was rendered.
	CreatedAt time.Time
}

// Step is one node in the plan.
type Step struct {
	// ID is unique within the Plan.
	ID string
	// Description is the human-readable action.
	Description string
	// Tool names the [tools.Tool] the step will invoke. Empty means
	// the step is a pure reasoning step (no tool).
	Tool string
	// Input is the JSON payload passed to the tool. Empty when Tool
	// is empty.
	Input []byte
	// ExpectedOutcome documents what "success" looks like.
	ExpectedOutcome string
}

// StepResult is what a Runner returns for one step.
type StepResult struct {
	// Output is the tool result (or free-form summary for reasoning steps).
	Output string
	// Snapshot is optional opaque state the caller wants preserved
	// with the step's checkpoint (e.g. serialised session bytes so a
	// later Rewind can restore it). Nil means "no snapshot" — Rewind
	// still works but only rewinds the plan cursor.
	Snapshot []byte
}

// Checkpoint captures per-step state so Rewind can restore it.
type Checkpoint struct {
	PlanID    string
	StepID    string
	AtStep    int
	Output    string
	Snapshot  []byte
	CreatedAt time.Time
}

// Runner executes one Plan step. Implementations wire this to
// whichever tool/agent primitive actually runs the work.
type Runner interface {
	// Execute runs step and returns its result.
	Execute(ctx context.Context, step Step) (StepResult, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, step Step) (StepResult, error)

// Execute satisfies Runner.
func (f RunnerFunc) Execute(ctx context.Context, step Step) (StepResult, error) {
	return f(ctx, step)
}

// ApprovalDecision is what an approval gate returns per step.
type ApprovalDecision int

const (
	// ApprovalApprove runs the step.
	ApprovalApprove ApprovalDecision = iota
	// ApprovalReject stops execution before the step runs (no
	// checkpoint is recorded).
	ApprovalReject
	// ApprovalSkip records a synthetic checkpoint and moves on.
	ApprovalSkip
)

// Options configures the Executor's per-step hooks.
type Options struct {
	// Runner runs individual steps. Required.
	Runner Runner
	// Checkpoints persists per-step checkpoints. Required.
	Checkpoints CheckpointStore
	// Approve is called before each step. Nil approves every step.
	Approve func(ctx context.Context, step Step) ApprovalDecision
	// OnStepComplete fires after each step's checkpoint is stored.
	OnStepComplete func(ctx context.Context, step Step, res StepResult)
}

// CheckpointStore persists per-plan checkpoints. Implementations must
// be safe for concurrent use.
type CheckpointStore interface {
	Append(ctx context.Context, cp Checkpoint) error
	// List returns the checkpoints for a plan in the order Append
	// received them.
	List(ctx context.Context, planID string) ([]Checkpoint, error)
	// TruncateAfter drops every checkpoint whose AtStep > afterStep.
	// Used by Rewind.
	TruncateAfter(ctx context.Context, planID string, afterStep int) error
}

// Executor drives a Plan through its Runner + CheckpointStore.
type Executor struct {
	opts Options
}

// New returns an Executor for the supplied options.
func New(opts Options) (*Executor, error) {
	if opts.Runner == nil {
		return nil, errors.New("agent/plan: Runner is required")
	}
	if opts.Checkpoints == nil {
		return nil, errors.New("agent/plan: Checkpoints is required")
	}
	return &Executor{opts: opts}, nil
}

// Result is what Run returns once the plan finishes (or the first
// non-nil error / rejection).
type Result struct {
	Plan            Plan
	CompletedSteps  int
	LastOutput      string
	Rejected        bool
	RejectedAtStep  int
}

// ErrRejected is returned when an approval gate rejects a step. Sits
// alongside Result.Rejected=true so callers can distinguish
// intentional stops from execution failures.
var ErrRejected = errors.New("agent/plan: step rejected by approval gate")

// Run executes every step in plan.Steps in order, honouring approval
// gates and recording a checkpoint after each successful step. Any
// error from a Runner or CheckpointStore stops execution — the plan
// is *not* rewound automatically (callers decide whether to Rewind).
func (e *Executor) Run(ctx context.Context, p Plan) (Result, error) {
	res := Result{Plan: p}
	for i, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if e.opts.Approve != nil {
			switch e.opts.Approve(ctx, step) {
			case ApprovalReject:
				res.Rejected = true
				res.RejectedAtStep = i
				return res, ErrRejected
			case ApprovalSkip:
				cp := Checkpoint{
					PlanID:    p.ID,
					StepID:    step.ID,
					AtStep:    i,
					Output:    "(skipped by approval gate)",
					CreatedAt: time.Now().UTC(),
				}
				if err := e.opts.Checkpoints.Append(ctx, cp); err != nil {
					return res, fmt.Errorf("agent/plan: checkpoint skip step %d: %w", i, err)
				}
				res.CompletedSteps++
				continue
			case ApprovalApprove:
				// fallthrough to execute
			}
		}
		out, err := e.opts.Runner.Execute(ctx, step)
		if err != nil {
			return res, fmt.Errorf("agent/plan: step %d (%s): %w", i, step.ID, err)
		}
		cp := Checkpoint{
			PlanID:    p.ID,
			StepID:    step.ID,
			AtStep:    i,
			Output:    out.Output,
			Snapshot:  out.Snapshot,
			CreatedAt: time.Now().UTC(),
		}
		if err := e.opts.Checkpoints.Append(ctx, cp); err != nil {
			return res, fmt.Errorf("agent/plan: checkpoint step %d: %w", i, err)
		}
		res.CompletedSteps++
		res.LastOutput = out.Output
		if e.opts.OnStepComplete != nil {
			e.opts.OnStepComplete(ctx, step, out)
		}
	}
	return res, nil
}

// Rewind drops the last n checkpoints for planID. Passing n <= 0 is
// a no-op. Returns the number of checkpoints that remain so the
// caller can compute the resume index.
func (e *Executor) Rewind(ctx context.Context, planID string, n int) (int, error) {
	if n <= 0 {
		cps, err := e.opts.Checkpoints.List(ctx, planID)
		if err != nil {
			return 0, err
		}
		return len(cps), nil
	}
	existing, err := e.opts.Checkpoints.List(ctx, planID)
	if err != nil {
		return 0, err
	}
	keep := len(existing) - n
	if keep < 0 {
		keep = 0
	}
	// The last kept checkpoint's AtStep is the truncation boundary.
	afterStep := -1
	if keep > 0 {
		afterStep = existing[keep-1].AtStep
	}
	if err := e.opts.Checkpoints.TruncateAfter(ctx, planID, afterStep); err != nil {
		return 0, err
	}
	return keep, nil
}

// Resume runs the remaining steps in p starting after the last
// stored checkpoint. Combines Rewind's cursor with Run.
func (e *Executor) Resume(ctx context.Context, p Plan) (Result, error) {
	existing, err := e.opts.Checkpoints.List(ctx, p.ID)
	if err != nil {
		return Result{Plan: p}, err
	}
	start := 0
	if len(existing) > 0 {
		start = existing[len(existing)-1].AtStep + 1
	}
	if start >= len(p.Steps) {
		return Result{Plan: p, CompletedSteps: len(p.Steps)}, nil
	}
	remaining := Plan{
		ID:        p.ID,
		Goal:      p.Goal,
		Steps:     p.Steps[start:],
		CreatedAt: p.CreatedAt,
	}
	res, runErr := e.Run(ctx, remaining)
	res.CompletedSteps += start
	res.Plan = p
	return res, runErr
}

// ---- MemoryCheckpointStore ------------------------------------------

// NewMemoryCheckpointStore returns an in-memory CheckpointStore.
func NewMemoryCheckpointStore() CheckpointStore {
	return &memoryCPStore{data: make(map[string][]Checkpoint)}
}

type memoryCPStore struct {
	mu   sync.Mutex
	data map[string][]Checkpoint
}

func (s *memoryCPStore) Append(_ context.Context, cp Checkpoint) error {
	if cp.PlanID == "" {
		return errors.New("agent/plan: checkpoint PlanID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[cp.PlanID] = append(s.data[cp.PlanID], cp)
	return nil
}

func (s *memoryCPStore) List(_ context.Context, planID string) ([]Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.data[planID]
	out := make([]Checkpoint, len(src))
	copy(out, src)
	return out, nil
}

func (s *memoryCPStore) TruncateAfter(_ context.Context, planID string, afterStep int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.data[planID]
	kept := src[:0]
	for _, cp := range src {
		if cp.AtStep <= afterStep {
			kept = append(kept, cp)
		}
	}
	s.data[planID] = kept
	return nil
}
