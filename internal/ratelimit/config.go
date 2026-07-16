package ratelimit

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Rate carries the parsed "Nr/Duration" form used in config. Nr is
// the requests-per-window, Window is the window itself. Refill rate
// as tokens/second is Nr / Window.Seconds().
type Rate struct {
	Requests float64
	Window   time.Duration
}

// RefillPerSec is the refill rate in tokens/second.
func (r Rate) RefillPerSec() float64 {
	if r.Window <= 0 {
		return 0
	}
	return r.Requests / r.Window.Seconds()
}

// ParseRate accepts "Nr/Duration" — e.g. "10r/1m", "60r/1s", "5r/5m".
// The "r" suffix is optional but recommended for readability. Duration
// is parsed by [time.ParseDuration]. Whitespace around the slash is
// tolerated.
func ParseRate(s string) (Rate, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rate{}, fmt.Errorf("ratelimit: empty rate string")
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return Rate{}, fmt.Errorf("ratelimit: bad rate %q (want Nr/Duration)", s)
	}
	nPart := strings.TrimSpace(strings.TrimSuffix(parts[0], "r"))
	n, err := strconv.ParseFloat(nPart, 64)
	if err != nil {
		return Rate{}, fmt.Errorf("ratelimit: bad rate count in %q: %w", s, err)
	}
	if n <= 0 {
		return Rate{}, fmt.Errorf("ratelimit: rate count must be > 0 in %q", s)
	}
	d, err := time.ParseDuration(strings.TrimSpace(parts[1]))
	if err != nil {
		return Rate{}, fmt.Errorf("ratelimit: bad duration in %q: %w", s, err)
	}
	if d <= 0 {
		return Rate{}, fmt.Errorf("ratelimit: duration must be > 0 in %q", s)
	}
	return Rate{Requests: n, Window: d}, nil
}

// MustParseRate is [ParseRate] that panics on error. Intended for
// package-internal defaults and test fixtures where a bad rate is a
// programmer bug.
func MustParseRate(s string) Rate {
	r, err := ParseRate(s)
	if err != nil {
		panic(err) //nolint:forbidigo // MustParseRate is a documented programmer-error path
	}
	return r
}
