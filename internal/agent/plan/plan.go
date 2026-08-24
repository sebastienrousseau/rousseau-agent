// Package plan scaffolds a checkpointed multi-step agent plan on top
// of the linear [agent.Turn] loop. Matches the "planning phase before
// execution" idiom Aider (architect mode), Cline (plan mode), and
// OpenCode (plan agent) all ship.
//
// A Plan is a DAG of Steps produced by an initial LLM call, then
// executed one step at a time with per-step approval gates. Every
// step boundary is a checkpoint: `/rewind 3` restores state to three
// steps back and continues from there.
//
// # Status
//
// Scaffold only. The types + Runner interface are in place; the
// plan renderer, the checkpoint store, and the `/plan` chat command
// land as W4.2 in the roadmap.
package plan

import (
	"context"
	"errors"
	"time"
)

// Plan is the sequenced set of steps to execute.
type Plan struct {
	// ID is a UUID. Persisted alongside checkpoints.
	ID string
	// Goal is the user-authored request that produced this plan.
	Goal string
	// Steps are executed in slice order; the DAG shape is future
	// work — for the first cut every plan is a straight sequence.
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
	// ExpectedOutcome documents what "success" looks like for this
	// step. The renderer includes this in the plan for user review.
	ExpectedOutcome string
}

// Checkpoint captures per-step state so `/rewind` can restore it.
type Checkpoint struct {
	PlanID    string
	StepID    string
	AtStep    int
	Snapshot  []byte // opaque serialisation of the session state
	CreatedAt time.Time
}

// Runner executes a Plan step-by-step and interacts with a
// checkpoint store.
type Runner interface {
	// Execute runs step and returns the tool output (or empty for
	// reasoning steps). Called once per step in order.
	Execute(ctx context.Context, step Step) (string, error)
}

// ErrScaffold is returned by every constructor in this package
// while the runtime is being built.
var ErrScaffold = errors.New("agent/plan: runtime not yet implemented (see docs/plan-mode.md)")

// New returns a Runner backed by the configured provider. Scaffold —
// returns ErrScaffold until W4.2 lands.
func New() (Runner, error) { return nil, ErrScaffold }
