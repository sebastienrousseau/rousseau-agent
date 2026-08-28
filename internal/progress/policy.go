package progress

import "time"

// Throttle defaults. The reasoning behind each number is in
// docs/progress-updates.md; the short version is inline here so the
// values are not silently retuned by someone reading only the code.
const (
	// DefaultFirstDelay is how long a turn may run before the first
	// progress message. WhatsApp already draws a "typing…" bubble and
	// clients drop it around 10s of silence, so 8s slots into the seam:
	// late enough that short turns never post progress at all, early
	// enough that the user never sees dead air.
	DefaultFirstDelay = 8 * time.Second
	// DefaultMinInterval is the floor between progress messages that
	// arrive as NEW messages (i.e. as phone notifications). At 25s a
	// five-minute task costs eleven notifications; at 10s it costs
	// thirty, which is spam.
	DefaultMinInterval = 25 * time.Second
	// DefaultMinEditInterval is the floor between in-place edits.
	// An edit is silent, so it can be 2.5x more frequent for free.
	DefaultMinEditInterval = 10 * time.Second
	// DefaultHeartbeatInterval is the floor between updates when
	// NOTHING new has happened. A single four-minute shell command
	// emits no events at all, and silence is the failure being fixed.
	DefaultHeartbeatInterval = 90 * time.Second
	// DefaultMaxUpdates caps progress updates per turn. Past the cap
	// the coalescer degrades to heartbeat-only so a pathological turn
	// cannot flood a thread.
	DefaultMaxUpdates = 20
	// DefaultPreviewChars caps how much streamed assistant text is
	// echoed back as a preview.
	DefaultPreviewChars = 120
	// DefaultMaxBullets caps the per-turn bullet log the renderer draws
	// above the spinner. Long turns keep only the most recent entries;
	// dropped-from-the-front is signalled by a leading "…" bullet so
	// the reader knows history was trimmed.
	DefaultMaxBullets = 12
)

// Policy is the coalescing + throttling configuration. The zero value
// is not usable directly; pass it through Normalise (or use
// DefaultPolicy) to fill in the defaults above.
type Policy struct {
	// FirstDelay is the silence a turn must accumulate before the
	// first progress update.
	FirstDelay time.Duration
	// MinInterval is the minimum gap between progress updates
	// delivered as new messages.
	MinInterval time.Duration
	// MinEditInterval is the minimum gap between progress updates
	// delivered as in-place edits. Ignored when PreferEdit is false.
	MinEditInterval time.Duration
	// HeartbeatInterval is the minimum gap between updates when no new
	// events have arrived since the last one.
	HeartbeatInterval time.Duration
	// MaxUpdates caps non-terminal updates per turn. Zero uses
	// DefaultMaxUpdates; negative means unlimited.
	MaxUpdates int
	// PreviewChars caps the streamed-text preview length.
	PreviewChars int
	// MaxBullets caps the per-turn bullet log. Zero uses
	// DefaultMaxBullets; negative means unlimited (only sane in tests).
	MaxBullets int
	// PreferEdit tells the coalescer that updates after the first will
	// be delivered by editing the first message in place, which
	// relaxes the throttle to MinEditInterval. The Reporter sets this
	// from whether its Sink implements Editor.
	PreferEdit bool
}

// DefaultPolicy returns the shipped throttle policy.
func DefaultPolicy() Policy {
	return Policy{}.Normalise()
}

// Normalise returns a copy of p with every zero-valued knob replaced
// by its default. Non-zero values are preserved verbatim, so a caller
// may override one knob without restating the rest.
func (p Policy) Normalise() Policy {
	if p.FirstDelay <= 0 {
		p.FirstDelay = DefaultFirstDelay
	}
	if p.MinInterval <= 0 {
		p.MinInterval = DefaultMinInterval
	}
	if p.MinEditInterval <= 0 {
		p.MinEditInterval = DefaultMinEditInterval
	}
	if p.HeartbeatInterval <= 0 {
		p.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if p.MaxUpdates == 0 {
		p.MaxUpdates = DefaultMaxUpdates
	}
	if p.PreviewChars <= 0 {
		p.PreviewChars = DefaultPreviewChars
	}
	if p.MaxBullets == 0 {
		p.MaxBullets = DefaultMaxBullets
	}
	return p
}
