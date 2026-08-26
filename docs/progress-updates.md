# Live progress + mid-flight interaction

**Status:** design + core runtime. Shipped in this change:
[`internal/progress`](../internal/progress) (event model, bus, coalescer,
renderer, reporter), [`internal/control`](../internal/control) (verb
taxonomy, parser, in-flight turn registry), agent-loop emission +
checkpoint gate, transport middleware, and WhatsApp delivery.

## The problem

`transport.Router.Handle` is synchronous: an inbound WhatsApp message
blocks inside `agent.Turn` until the model stops. A turn that runs nine
tool calls and two sub-agents can take four minutes. During those four
minutes the user sees:

1. a "typing…" bubble that WhatsApp clients drop after ~10 s, then
2. nothing.

Worse, `whatsmeow` drains its inbound node queue on **one** goroutine
(`handlerQueueLoop`, `client.go:861`) and dispatches message events
**synchronously** (`dispatchEvent`, `client.go:923`). A blocking handler
therefore stalls every subsequent inbound message for the whole turn —
so today it is not merely that the user *isn't told* what's happening,
it is that the user *cannot say anything* until it's over. Mid-flight
interaction is impossible by construction.

Two things must change: the agent must **emit** progress, and the
transport must stop blocking its receive path.

Both are always-on. There is no `progress.enabled` flag: a chat agent
that goes silent for four minutes is broken, and an opt-in fix for a
broken default is not a fix.

## a) Progress event model

### Reuse, not reinvention

`agent.StreamEvent` already exists (`internal/agent/stream.go`) and
`claudecli.Provider.Stream` already produces it. It is the right
primitive for *one provider round-trip* and the wrong one for *a turn*:
it has no notion of tool identity, iteration index, sub-agent, plan
step, or cron job, and it is scoped to a single `Stream` call while a
turn makes up to `MaxIterations` of them.

So: `agent.StreamEvent` is kept as-is and **lifted** into the progress
model. `agent.TurnStream` translates each `StreamEvent` it forwards into
a `progress.Event` (`StreamTextDelta` → `KindLLMDelta`, `StreamToolUse`
→ `KindToolStarted`) and publishes it on the same bus that the
non-streaming `Turn` publishes to. Nothing downstream of the bus knows
or cares which provider path produced the event.

### The event

```go
type Event struct {
    Key       string        // conversation key (routing)
    Kind      Kind
    Tool      string        // KindTool*
    Text      string        // headline / delta / final answer
    Detail    string
    Iteration int           // agent-loop round-trip, 1-based
    Step, Of  int           // plan step n of m
    Elapsed   time.Duration
    Err       string
    At        time.Time
}
```

### Emitters

| Stage | Kind | Emitted by |
|---|---|---|
| turn accepted | `KindTurnStarted` | `agent.Turn` / `agent.TurnStream` |
| model round-trip begins | `KindThinking` | agent loop, per iteration |
| assistant text streaming | `KindLLMDelta` | `agent.TurnStream` (from `StreamTextDelta`) |
| tool about to run | `KindToolStarted` | `agent.runTools` |
| tool returned | `KindToolFinished` | `agent.runTools` (carries `Err` on failure) |
| tool blocked | `KindToolDenied` | `agent.runTools` (approver / hook deny) |
| sub-agent dispatched | `KindSubagentStarted` | `subagent.Spawn` via `Policy.Progress` |
| sub-agent done | `KindSubagentFinished` | `subagent.Spawn` |
| plan step boundary | `KindPlanStep` | `plan.Options.OnStepComplete` adapter |
| cron job fired | `KindCronStarted` / `KindCronFinished` | `cron.Scheduler` |
| user paused / resumed | `KindPaused` / `KindResumed` | `control.Turn` |
| user cancelled | `KindCancelled` | `control.Turn` |
| turn finished | `KindTurnFinished` | agent loop (terminal) |
| turn failed | `KindError` | agent loop (terminal) |

`KindTurnFinished`, `KindError` and `KindCancelled` are **terminal**:
they force an immediate flush regardless of throttle state.

### Routing key

The agent does not know a WhatsApp JID and the transport does not know a
session ID. Neither should learn. The key rides the context:
`progress.WithKey(ctx, key)` is set by the transport middleware to the
conversation identifier it already has (`msg.From`), and the agent reads
it back with `progress.KeyFrom(ctx)`, falling back to `session.ID` when
absent (CLI, tests, embedded use). One mechanism, every transport.

### Bus

`progress.Bus` is a fan-out keyed by conversation. `Publish` is
**non-blocking**: each subscriber has a bounded ring (default 64) and a
full ring drops the *oldest* event and increments a per-subscriber
counter. The dropped count is rendered as `…` in the next update. The
agent loop must never block on a chat transport — an agent that stalls
because WhatsApp is slow is a worse failure than a lossy progress feed,
and progress is by definition lossy-tolerant: the coalescer only ever
renders the *latest* state, so a dropped intermediate event changes
nothing the user would have seen.

## b) Coalescing and throttling

WhatsApp is not a terminal. A 4-minute turn emits on the order of
10²–10³ events; it must produce **≈10 messages**, not 200.

### The state machine

Events are folded into a single `progress.State` (running tools,
completed count, sub-agent count, plan cursor, last text preview,
dropped count). `Coalescer.Absorb` is a pure fold; `Coalescer.Next(now)`
is a pure decision function. No timers, no goroutines, no wall-clock
reads inside — which is what makes the policy testable to the second
without sleeping.

### The numbers

| Knob | Value | Why |
|---|---|---|
| `FirstDelay` (**N**) | **8 s** | WhatsApp already draws a "typing…" bubble, and clients drop it at roughly 10 s of silence. Emitting before 8 s duplicates an indicator the user can already see; emitting after 10 s leaves a visible dead gap. 8 s slots into the seam. It is also above the p50 turn latency for chat-shaped requests — the common "hi" → "hey" turn finishes without ever posting a progress line, which is the whole point. |
| `MinInterval` (**M**, new message) | **25 s** | Each new message is a phone notification. At 25 s a 5-minute task costs 11 notifications — noticeable but tolerable; at 10 s it costs 30, which is spam and gets the bot muted. 25 s is also under the ~30 s at which a human starts wondering whether the thing has died. |
| `MinEditInterval` | **10 s** | An **edit** is not a notification — the message updates silently in place. Where the transport supports it, refresh 2.5× more often for free. WhatsApp supports edits (`whatsmeow` `BuildEdit`) within a 15-minute window; Telegram, Slack and Discord support them outright. |
| `HeartbeatInterval` | **90 s** | If nothing *new* has happened we normally stay silent. But a single 4-minute `bash` produces no events at all, and silence is exactly the failure we set out to fix. So: with no new material, re-emit at most every 90 s, with the elapsed clock ticking, so the user can distinguish "working" from "hung". |
| `MaxUpdates` | **20** | Hard ceiling per turn. Past 20 the reporter degrades to heartbeat-only. A pathological turn cannot flood a thread. |

### Edit-in-place

The reporter posts progress update #1 as a new message and keeps its
handle. If the sink implements `progress.Editor`, every subsequent
update **edits that message**; the thread shows one live status line
rather than a wall of them. If an edit fails (stale handle, past the
provider's edit window) the reporter drops the handle and posts a new
message — degradation, not failure.

The **final answer is always a new message.** It must ping. The progress
line is edited one last time to a collapsed epitaph
(`✅ done in 4m12s · 9 tools`) so scrollback stays readable.

### Worked example

A 4m10s turn with 9 tool calls, with edit support:
message #1 at t=8 s, then edits at 18/28/…/250 s ≈ 24 edits (capped at
20, then 90 s heartbeats), and one final answer message. **Two
notifications total.** Without edit support: 8 s, 33 s, 58 s, … ≈ 10
progress messages plus the final.

## c) Interaction verbs

### What ships in the first cut

Four commands, recognised **only** in their explicit slash form:

```
/status    what is the turn doing right now
/pause     suspend at the next checkpoint
/resume    release a paused turn
/cancel    abort the turn
```

Nothing else interrupts. Bare `cancel`, `stop`, `wait`, `continue`,
`hold on`, `nevermind` are ordinary prompt text and are steered into the
running turn like any other message.

### Why slash-only

The obvious design also accepts bare words. It reads better and needs no
syntax. It is also where every false positive lives.

The costs are not symmetric, and it is not close:

- **False positive** (user typed content, we read it as control): the
  user's actual request is silently discarded and, for `cancel`, however
  many minutes of work were in flight are destroyed. Unrecoverable
  without the user noticing and retyping.
- **False negative** (user typed control, we read it as content): the
  message is steered into the running turn as an instruction. Mildly
  wrong, instantly recoverable — send it again with a slash.

An unrecoverable failure on one side and an annoyance on the other means
the classifier must be heavily biased toward "prompt". The bare-word
form was prototyped with guard rails — a running-turn requirement, a
three-word ceiling, filler stripping, quote and code-fence detection —
and it worked. It was still dropped, because "wait", "stop", "hold on"
and "continue" are ordinary English that appears constantly mid-request,
and no word-count heuristic separates the command from the aside
reliably enough to bet a user's work on it.

A slash prefix costs one character and makes the false-positive rate
**zero by construction** rather than by tuning. That is the right trade
for a first cut, when there is no usage data to tune against.

### Which verbs, and why not more

The structural test for admitting a verb: **it refers to the agent's own
execution and takes no object.** `cancel` is complete on its own;
`summarise` demands "summarise *what*", and anything demanding an object
is a request for new work rather than a command about work in progress.

That test admits `status`, `pause`, `resume`, `cancel` — and arguably
`explain`. `explain` was rejected: bare "explain" plausibly means
"explain what you are doing" *or* "explain the thing we were just
discussing", and that ambiguity is precisely what this design is trying
not to have. `/status` covers the useful half.

The wider verb list (summarise, translate, analyse, brainstorm, and the
rest) is deliberately **not** modelled. Those verbs never interrupt, so
classifying them changes no behaviour — it only labels telemetry. A
sixty-entry table that alters nothing is maintenance cost and a
misclassification surface for no functional gain, so it is deferred
until something actually consumes the labels (per-verb routing to a
cheaper model is the obvious candidate).

### Adding aliases later

Bare aliases are strictly easier to add than to remove: adding one is a
new entry in `slashVerbs`, while removing one after it has silently
eaten somebody's work costs trust that is hard to win back. The intended
sequence is to ship slash-only, log what people actually type when a
turn is running, and promote aliases that show up often and never appear
mid-sentence.

`internal/control.Decide` deliberately takes no "is a turn running"
parameter. Making classification depend on timing would mean identical
text meaning two different things depending on when it arrived, which is
close to impossible to reason about from a bug report.

## d) Concurrency model

```
whatsmeow handlerQueueLoop (1 goroutine, must never block)
  └─ Client.handleMessage  ──dispatch──▶  goroutine per message
                                            │
                                   transport.Handler chain
                                     ratelimit → Recover
                                       → control middleware
                                         → Router → agent.Turn
```

**Unblocking the receive path.** `whatsmeow` dispatches synchronously on
a single goroutine, so `Client.handleMessage` now hands each message to a
`dispatch func(func())` seam: inline by default (which is what unit
tests construct), `go f()` once `Start` has run. That one change is what
makes every later step possible — without it a second message cannot
even be parsed until the first turn ends.

**Reaching a running turn.** `control.Registry` maps conversation key →
`*control.Turn`. The middleware:

```go
if cmd, ok := control.Parse(body); ok && reg.Running(key) {
    return reg.Apply(ctx, key, cmd)      // never touches the LLM
}
if t, ok := reg.Lookup(key); ok {
    t.Steer(body); return "added to the current run."
}
ctx, turn := reg.Begin(ctx, key)          // ctx carries key + gate
defer turn.End()
return next.Handle(ctx, msg)
```

Control replies are produced synchronously by the registry — they are
free, instant, and never enter the agent loop.

**Reaching *into* the loop.** `Begin` puts a `agent.TurnControl` on the
context. The loop calls `Checkpoint(ctx)` at every iteration boundary
and between tool calls. `Checkpoint`:

- returns `ErrCancelled` if the turn was cancelled,
- **blocks** while paused (until resume, cancel, or `ctx.Done()`),
- returns steered messages for the loop to append.

**Cancellation propagates on two levels.** `Begin` derives a cancellable
context; `Cancel` cancels it, which aborts the in-flight provider call
immediately (`claudecli` uses `exec.CommandContext`, so the child
`claude` process is signalled) and unwinds the loop with `context.
Canceled`. `Checkpoint` is the second level, for the window between
provider calls where there is no I/O to interrupt. Pause has only the
second level and is documented as such: **pause takes effect at the next
checkpoint, not mid-token.** Claiming otherwise would be a lie the
implementation cannot keep.

**Progress delivery** is a third goroutine per turn: the transport
subscribes to the bus for the key, runs `progress.Reporter` until the
handler returns, then unsubscribes. Reporter owns the coalescer, the
ticker, and the sink. The agent never touches a socket.

## e) Transport failure mid-progress

Progress is best-effort telemetry, not part of the turn's contract.

- A failed progress send is logged at `Debug`, counted, and ignored. It
  never aborts, retries, or slows the turn.
- **Three consecutive** send failures trip the reporter's breaker: it
  stops sending progress for the remainder of the turn. It still
  attempts the terminal update, because that is the one the user
  actually needs.
- A failed **edit** clears the stored handle; the next update posts a new
  message.
- On disconnect, progress is **dropped, never buffered**. Replaying a
  four-minute-old "running bash" after reconnect actively misinforms.
- The final answer does not travel this path. It is returned by
  `Handler.Handle` and delivered by the transport's existing send, and
  the session is persisted by the Router regardless — so a progress
  outage costs the user visibility, never work.
- If the *turn* fails, the reporter emits a terminal `KindError` update
  so the user is told, rather than watching an update clock freeze.
