# Plan mode with checkpoints (W4.2)

**Status:** [`internal/agent/plan`](../internal/agent/plan) package
skeleton shipped in `v0.0.1`; runtime is a follow-up.

## Why

The current agent loop is linear: prompt → complete → maybe run
tools → loop until end_turn. Works well for chat-shaped requests;
awkward for "do these five things in order, letting me approve each
one" requests.

Aider (architect mode), Cline (plan mode), and OpenCode (plan
agent) all ship a distinct planning phase that produces a
read-only plan before any tool runs. LangGraph's checkpointing +
time-travel is the production pattern for multi-step orchestration.

## Design

### `/plan <goal>` chat command

The transport router recognises `/plan` as the first token and
routes to the plan generator instead of the regular agent loop.

```
user: /plan refactor internal/config into per-subsystem files
agent: (renders a 5-step plan as structured text)
user: /plan approve            # or `/plan edit 3` to revise a step
agent: (executes step-by-step; asks for approval between steps
        when tools.bash.approver requires it)
```

### Plan representation

Already typed in [`internal/agent/plan`](../internal/agent/plan):

```go
type Plan struct {
    ID string; Goal string
    Steps []Step
    CreatedAt time.Time
}
type Step struct {
    ID string; Description string; Tool string
    Input []byte; ExpectedOutcome string
}
```

### Checkpoints + rewind

Before each Step executes, snapshot the session state to
`plan_checkpoints`:

```sql
CREATE TABLE plan_checkpoints (
    plan_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    at_step     INTEGER NOT NULL,
    snapshot    BLOB NOT NULL,          -- gob-encoded agent.Session
    created_at  TEXT NOT NULL,
    PRIMARY KEY (plan_id, step_id)
);
```

`/rewind N` looks up the checkpoint N steps back, restores it, and
resumes execution.

### Approver integration

Plan steps whose Tool is bash / write / edit route through the same
[`agent.Approver`](../internal/agent/approver.go) that regular
tool calls use. A hook can also fire per step.

## Cost model

Two extra provider calls per plan: one to produce the plan, one to
consume the results (already counted). Rewind costs one extra
provider call per rewind. Documented in `docs/benchmarks/README.md`
cost table.

## Success metric

"Refactor these 3 files, run tests, commit" produces a 5-step
plan; each step is executed with the user's `/plan approve` between
tool calls; `/rewind 2` correctly restores state before the commit
so a re-run of the last two steps produces a different commit.
