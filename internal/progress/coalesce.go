package progress

import "time"

// State is the folded view of every event seen so far for one turn.
// It is what gets rendered — the Coalescer never renders history, only
// the latest state, which is why dropping intermediate events is safe.
type State struct {
	// Key is the conversation this state belongs to.
	Key string
	// Iteration is the current agent-loop round-trip (1-based).
	Iteration int
	// Running lists the tools currently executing, in start order.
	Running []string
	// ToolsDone counts tools that returned (successfully or not).
	ToolsDone int
	// ToolsFailed counts tools that returned an error.
	ToolsFailed int
	// ToolsDenied counts tools blocked by an approver or hook.
	ToolsDenied int
	// SubagentsRunning / SubagentsDone track sub-agent fan-out.
	SubagentsRunning int
	SubagentsDone    int
	// Step and Of carry the plan cursor; both zero when no plan runs.
	Step, Of int
	// Cron names the scheduled job driving this turn, if any.
	Cron string
	// Preview is the tail of the assistant text streamed so far.
	Preview string
	// Steers counts mid-flight user injections.
	Steers int
	// Paused reports whether the user has paused the turn.
	Paused bool
	// Dropped is how many events the transport's ring buffer lost.
	Dropped int
	// Terminal is set once a terminal event has been folded in.
	Terminal bool
	// Outcome is the terminal Kind (KindTurnFinished, KindError,
	// KindCancelled) once Terminal is set.
	Outcome Kind
	// Err carries the failure text for a terminal KindError.
	Err string
	// Bullets is the ordered log of one action per line the turn has
	// taken so far — the render draws them with a leading glyph
	// (● success, ✗ failure, ⊘ denied) above the spinner, mirroring
	// the Claude CLI feed. Bounded by Policy.MaxBullets; a trim leaves
	// a leading "…" marker bullet so the reader knows history is
	// incomplete.
	Bullets []Bullet
	// bulletsTrimmed is true once at least one bullet has fallen off
	// the front of the log. The renderer uses it to draw a "…" marker.
	bulletsTrimmed bool
}

// Bullet is one entry in State.Bullets.
type Bullet struct {
	// Text is the human-readable action ("Read foo.go", "Bash go test",
	// "Grep for `handleTextMessage`"). Never carries a leading glyph —
	// the renderer picks the right one from Failed/Denied.
	Text string
	// Failed is true when the action returned an error.
	Failed bool
	// Denied is true when the action was blocked by an approver or hook.
	Denied bool
}

// Update is one rendered progress message ready for a Sink.
type Update struct {
	// Key is the conversation to deliver to.
	Key string
	// Text is the fully rendered message body.
	Text string
	// Seq is the 1-based ordinal of this update within the turn.
	Seq int
	// Elapsed is how long the turn has been running.
	Elapsed time.Duration
	// Replace asks the Sink to edit the previously-sent progress
	// message rather than posting a new one.
	Replace bool
	// Terminal marks the closing update of a turn.
	Terminal bool
}

// Coalescer folds a stream of Events into a State and decides when
// that State is worth sending.
//
// It is a pure state machine: Absorb mutates, Next decides, and the
// only notion of time is the value the caller passes in. No timers, no
// goroutines, no wall-clock reads — which is what lets the throttle
// policy be tested to the second without sleeping.
//
// A Coalescer is NOT safe for concurrent use; the Reporter owns one
// and drives it from a single goroutine.
type Coalescer struct {
	pol       Policy
	st        State
	started   time.Time
	lastEmit  time.Time
	emitted   int
	dirty     bool
	finalSent bool
}

// NewCoalescer constructs a Coalescer for key, treating start as the
// moment the turn began.
func NewCoalescer(key string, pol Policy, start time.Time) *Coalescer {
	return &Coalescer{
		pol:     pol.Normalise(),
		st:      State{Key: key},
		started: start,
	}
}

// State returns the current folded state.
func (c *Coalescer) State() State { return c.st }

// Emitted reports how many updates have been produced so far.
func (c *Coalescer) Emitted() int { return c.emitted }

// SetDropped records how many events were lost in transit so the next
// update can admit it.
func (c *Coalescer) SetDropped(n int) {
	if n != c.st.Dropped {
		c.st.Dropped = n
		c.dirty = true
	}
}

// Absorb folds ev into the coalesced state.
func (c *Coalescer) Absorb(ev Event) {
	c.dirty = true
	if ev.Iteration > 0 {
		c.st.Iteration = ev.Iteration
	}
	switch ev.Kind {
	case KindTurnStarted:
		// Nothing beyond marking the state dirty: the renderer's
		// "working…" fallback covers it.
	case KindThinking:
		c.st.Preview = ""
	case KindLLMDelta:
		c.st.Preview = tail(c.st.Preview+ev.Text, c.pol.PreviewChars)
	case KindToolStarted:
		c.st.Running = append(c.st.Running, ev.Tool)
	case KindToolFinished:
		c.st.Running = removeFirst(c.st.Running, ev.Tool)
		c.st.ToolsDone++
		if ev.Err != "" {
			c.st.ToolsFailed++
		}
		c.appendBullet(Bullet{Text: bulletText(ev), Failed: ev.Err != ""})
	case KindToolDenied:
		c.st.Running = removeFirst(c.st.Running, ev.Tool)
		c.st.ToolsDenied++
		c.appendBullet(Bullet{Text: bulletText(ev), Denied: true})
	case KindSubagentStarted:
		c.st.SubagentsRunning++
	case KindSubagentFinished:
		if c.st.SubagentsRunning > 0 {
			c.st.SubagentsRunning--
		}
		c.st.SubagentsDone++
		c.appendBullet(Bullet{Text: subagentBulletText(ev), Failed: ev.Err != ""})
	case KindPlanStep:
		c.st.Step, c.st.Of = ev.Step, ev.Of
	case KindCronStarted:
		c.st.Cron = ev.Text
	case KindCronFinished:
		c.st.Cron = ""
	case KindPaused:
		c.st.Paused = true
	case KindResumed:
		c.st.Paused = false
	case KindSteered:
		c.st.Steers++
	case KindTurnFinished, KindError, KindCancelled:
		c.st.Terminal = true
		c.st.Outcome = ev.Kind
		c.st.Err = ev.Err
		c.st.Running = nil
	}
}

// Next reports whether the current state should be sent at now, and if
// so returns the rendered Update. Calling Next has no effect when it
// returns false, so it is safe to poll on a ticker.
//
// The rules, in order:
//
//  1. Once the terminal update has been produced, never emit again.
//  2. A terminal state flushes immediately — but ONLY if at least one
//     progress update was already sent. A turn fast enough never to
//     have shown progress must not be followed by an epitaph for
//     progress the user never saw.
//  3. The first update waits FirstDelay from the turn's start.
//  4. Later updates wait MinEditInterval (when editing in place) or
//     MinInterval (when posting new messages).
//  5. With no new events, updates wait HeartbeatInterval instead.
//  6. Past MaxUpdates, only heartbeats survive.
func (c *Coalescer) Next(now time.Time) (Update, bool) {
	if c.finalSent {
		return Update{}, false
	}
	if c.st.Terminal {
		if c.emitted == 0 {
			c.finalSent = true
			return Update{}, false
		}
		c.finalSent = true
		return c.build(now, true), true
	}
	if !c.ready(now) {
		return Update{}, false
	}
	return c.build(now, false), true
}

// ready applies rules 3-6.
func (c *Coalescer) ready(now time.Time) bool {
	if !c.dirty && c.emitted == 0 {
		return false
	}
	if c.emitted == 0 {
		return now.Sub(c.started) >= c.pol.FirstDelay
	}
	since := now.Sub(c.lastEmit)
	overCap := c.pol.MaxUpdates > 0 && c.emitted >= c.pol.MaxUpdates
	if !c.dirty || overCap {
		return since >= c.pol.HeartbeatInterval
	}
	if c.pol.PreferEdit {
		return since >= c.pol.MinEditInterval
	}
	return since >= c.pol.MinInterval
}

// build renders the current state and advances the emit bookkeeping.
func (c *Coalescer) build(now time.Time, terminal bool) Update {
	c.emitted++
	c.lastEmit = now
	c.dirty = false
	elapsed := now.Sub(c.started)
	return Update{
		Key:      c.st.Key,
		Text:     Render(c.st, elapsed),
		Seq:      c.emitted,
		Elapsed:  elapsed,
		Replace:  c.pol.PreferEdit && c.emitted > 1,
		Terminal: terminal,
	}
}

// appendBullet appends b to State.Bullets and trims to MaxBullets by
// dropping from the front so the newest entries always survive.
func (c *Coalescer) appendBullet(b Bullet) {
	if b.Text == "" {
		return
	}
	c.st.Bullets = append(c.st.Bullets, b)
	cap := c.pol.MaxBullets
	if cap > 0 && len(c.st.Bullets) > cap {
		drop := len(c.st.Bullets) - cap
		c.st.Bullets = append(c.st.Bullets[:0:0], c.st.Bullets[drop:]...)
		c.st.bulletsTrimmed = true
	}
}

// bulletText builds the human-readable line for a tool event. Prefers
// Event.Detail (which callers may set to "foo.go", "go test", etc.);
// falls back to just the tool name. Empty tool means empty bullet —
// appendBullet will drop it.
func bulletText(ev Event) string {
	if ev.Tool == "" {
		return ""
	}
	if d := oneLine(ev.Detail); d != "" {
		return ev.Tool + " " + d
	}
	return ev.Tool
}

// subagentBulletText builds the bullet for a finished sub-agent. The
// Detail field carries the task label when the publisher supplies one.
func subagentBulletText(ev Event) string {
	if d := oneLine(ev.Detail); d != "" {
		return "sub-agent " + d
	}
	return "sub-agent"
}

// removeFirst drops the first occurrence of name from xs, preserving
// order. Returns xs unchanged when name is absent.
func removeFirst(xs []string, name string) []string {
	for i, x := range xs {
		if x == name {
			return append(xs[:i:i], xs[i+1:]...)
		}
	}
	return xs
}

// tail returns the last n characters of s (rune-safe).
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
