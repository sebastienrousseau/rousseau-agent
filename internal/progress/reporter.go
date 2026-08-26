package progress

import (
	"context"
	"log/slog"
	"time"
)

// Handle is an opaque, transport-specific identifier for a message a
// Sink has already delivered. Sinks that support editing return one
// from Send and receive it back on Edit.
type Handle string

// Sink delivers rendered progress updates to a user. Implementations
// live next to their transport.
type Sink interface {
	// Send posts u as a new message and returns a Handle for it.
	// Returning an empty Handle is fine — it only disables editing.
	Send(ctx context.Context, u Update) (Handle, error)
}

// Editor is the optional in-place-update capability. A Sink that
// implements it lets the Reporter refresh one live status message
// instead of posting a new one each time, which is both quieter for
// the user and 2.5x more frequent (see Policy.MinEditInterval).
type Editor interface {
	Sink
	// Edit replaces the body of a previously-sent message.
	Edit(ctx context.Context, h Handle, u Update) error
}

// DefaultMaxFailures is how many consecutive Sink failures trip the
// Reporter's breaker.
const DefaultMaxFailures = 3

// DefaultResolution is how often the Reporter re-evaluates the
// throttle policy when the caller supplies no tick channel.
const DefaultResolution = time.Second

// ReporterConfig bundles a Reporter's collaborators.
type ReporterConfig struct {
	// Sub is the bus subscription to drain. Required.
	Sub *Subscription
	// Sink delivers updates. Required.
	Sink Sink
	// Policy configures coalescing. PreferEdit is overwritten from
	// whether Sink implements Editor.
	Policy Policy
	// Start is the moment the turn began. Zero uses Now().
	Start time.Time
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
	// Tick, when non-nil, drives policy re-evaluation instead of an
	// internally-owned ticker. Tests inject this to run the whole
	// throttle policy without sleeping.
	Tick <-chan time.Time
	// Resolution is the internal ticker period when Tick is nil.
	// Zero uses DefaultResolution.
	Resolution time.Duration
	// MaxFailures is the consecutive-failure breaker threshold.
	// Zero uses DefaultMaxFailures; negative disables the breaker.
	MaxFailures int
	// Logger receives send failures at Debug. Nil uses slog.Default.
	Logger *slog.Logger
}

// Reporter pumps one conversation's progress events into a Sink,
// applying the coalescing policy. One Reporter runs per in-flight
// turn; it owns its Coalescer and is not safe for concurrent use.
type Reporter struct {
	cfg      ReporterConfig
	co       *Coalescer
	editor   Editor
	handle   Handle
	failures int
	sent     int
}

// NewReporter constructs a Reporter. Run drives it.
func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Start.IsZero() {
		cfg.Start = cfg.Now()
	}
	if cfg.Resolution <= 0 {
		cfg.Resolution = DefaultResolution
	}
	if cfg.MaxFailures == 0 {
		cfg.MaxFailures = DefaultMaxFailures
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	r := &Reporter{cfg: cfg}
	if ed, ok := cfg.Sink.(Editor); ok {
		r.editor = ed
		cfg.Policy.PreferEdit = true
	} else {
		cfg.Policy.PreferEdit = false
	}
	key := ""
	if cfg.Sub != nil {
		key = cfg.Sub.key
	}
	r.co = NewCoalescer(key, cfg.Policy, cfg.Start)
	return r
}

// Sent reports how many updates were successfully handed to the Sink.
func (r *Reporter) Sent() int { return r.sent }

// Run drains the subscription until it closes or ctx is cancelled,
// delivering updates as the policy allows. It blocks; callers run it
// on its own goroutine for the lifetime of the turn.
func (r *Reporter) Run(ctx context.Context) {
	if r.cfg.Sub == nil || r.cfg.Sink == nil {
		return
	}
	tick := r.cfg.Tick
	if tick == nil {
		t := time.NewTicker(r.cfg.Resolution)
		defer t.Stop()
		tick = t.C
	}
	events := r.cfg.Sub.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			r.co.Absorb(ev)
			r.co.SetDropped(r.cfg.Sub.Dropped())
			r.pump(ctx)
			if ev.Kind.Terminal() {
				return
			}
		case <-tick:
			r.co.SetDropped(r.cfg.Sub.Dropped())
			r.pump(ctx)
		}
	}
}

// pump asks the Coalescer whether an update is due and delivers it.
func (r *Reporter) pump(ctx context.Context) {
	u, ok := r.co.Next(r.cfg.Now())
	if !ok {
		return
	}
	r.deliver(ctx, u)
}

// deliver writes u to the Sink, editing in place when possible.
//
// Every failure here is swallowed: progress is best-effort telemetry
// and must never abort, retry into, or slow down a turn. After
// MaxFailures consecutive failures the Reporter stops trying entirely
// for non-terminal updates — but always attempts the terminal one,
// because that is the update the user actually needs.
func (r *Reporter) deliver(ctx context.Context, u Update) {
	if r.tripped() && !u.Terminal {
		return
	}
	if u.Replace && r.editor != nil && r.handle != "" {
		if err := r.editor.Edit(ctx, r.handle, u); err != nil {
			// A stale handle (past the transport's edit window, or a
			// deleted message) is the common case: drop it so the next
			// update posts a fresh message instead.
			r.handle = ""
			r.fail("progress.edit_failed", err)
			return
		}
		r.failures = 0
		r.sent++
		return
	}
	h, err := r.cfg.Sink.Send(ctx, u)
	if err != nil {
		r.fail("progress.send_failed", err)
		return
	}
	r.failures = 0
	r.sent++
	if r.handle == "" {
		r.handle = h
	}
}

// tripped reports whether the consecutive-failure breaker is open.
func (r *Reporter) tripped() bool {
	return r.cfg.MaxFailures > 0 && r.failures >= r.cfg.MaxFailures
}

func (r *Reporter) fail(msg string, err error) {
	r.failures++
	r.cfg.Logger.Debug(msg,
		slog.String("key", r.co.st.Key),
		slog.Int("consecutive", r.failures),
		slog.String("err", err.Error()),
	)
}
