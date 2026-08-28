// Package soak drives a long-running invariant-checking harness
// against synthetic transport traffic. The nightly CI run
// exercises this for 24 hours; PR runs use a 30-minute variant.
//
// Purpose: catch goroutine, FD, or memory leaks that unit tests miss
// because they run for milliseconds. Row 1 (Core correctness) of the
// scorecard requires wall-clock time evidence; this harness supplies
// it (§10 of docs/IMPLEMENTATION_PLAN_2026_07_16.md).
package soak

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Sample is one point-in-time snapshot of the daemon's resource
// footprint. Recorded every SampleInterval by [Monitor].
type Sample struct {
	At         time.Time
	Goroutines int
	AllocBytes uint64
	SysBytes   uint64
	HeapInUse  uint64
	FDs        int
	// GCPauseTotalNS is total STW pause time as of this sample. A
	// sudden slope change flags GC-pressure regressions.
	GCPauseTotalNS uint64
	// NumGC is the cumulative GC cycle count.
	NumGC uint32
}

// Monitor records runtime samples on a tick until Stop is called.
type Monitor struct {
	samples []Sample
	stopC   chan struct{}
	doneC   chan struct{}
	tick    time.Duration
}

// NewMonitor constructs a Monitor with the supplied sample interval.
// A zero interval defaults to 5 minutes.
func NewMonitor(interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Monitor{
		stopC: make(chan struct{}),
		doneC: make(chan struct{}),
		tick:  interval,
	}
}

// Start begins sampling in a goroutine. Not idempotent — call once.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop signals the monitor to exit and blocks until the loop returns.
// Returns the collected samples.
func (m *Monitor) Stop() []Sample {
	close(m.stopC)
	<-m.doneC
	return m.samples
}

func (m *Monitor) loop() {
	defer close(m.doneC)
	// Take the first sample immediately so short runs still have data.
	m.samples = append(m.samples, take())
	t := time.NewTicker(m.tick)
	defer t.Stop()
	for {
		select {
		case <-m.stopC:
			m.samples = append(m.samples, take())
			return
		case <-t.C:
			m.samples = append(m.samples, take())
		}
	}
}

// take reads current runtime + FD counts into a Sample.
func take() Sample {
	var ms runtime.MemStats
	// Soak invariants compare retained live memory, not transient heap
	// waiting for the next scheduled GC cycle.
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return Sample{
		At:             time.Now().UTC(),
		Goroutines:     runtime.NumGoroutine(),
		AllocBytes:     ms.Alloc,
		SysBytes:       ms.Sys,
		HeapInUse:      ms.HeapInuse,
		FDs:            countFDs(),
		GCPauseTotalNS: ms.PauseTotalNs,
		NumGC:          ms.NumGC,
	}
}

// countFDs walks /proc/self/fd on Linux; returns 0 elsewhere (macOS
// tests skip the FD invariant).
func countFDs() int {
	entries, err := os.ReadDir(filepath.Join("/proc", "self", "fd"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// Invariant is a boolean check applied to a slice of samples at the
// end of a soak run. Name identifies the invariant in the report;
// Passed is the boolean; Message is a short explanation on failure.
type Invariant struct {
	Name    string
	Passed  bool
	Message string
}

// CheckInvariants applies the standard leak-detection invariants:
//   - goroutine count within 20% of the 30-minute-mark baseline
//   - alloc bytes within 2× the baseline
//   - FD count within baseline + 10 (only on Linux where FDs != 0)
//
// The baseline sample is the one at or after 30 minutes into the
// run, so cold-start noise (session boot, GC ramp-up) is excluded.
// Runs shorter than baselineAfter use the first sample as baseline.
func CheckInvariants(samples []Sample, baselineAfter time.Duration) []Invariant {
	if len(samples) == 0 {
		return []Invariant{{Name: "no-samples", Passed: false, Message: "monitor recorded zero samples"}}
	}
	baseline := samples[0]
	origin := baseline.At
	for _, s := range samples {
		if s.At.Sub(origin) >= baselineAfter {
			baseline = s
			break
		}
	}
	final := samples[len(samples)-1]

	out := []Invariant{}

	goroutineLimit := int(float64(baseline.Goroutines) * 1.2)
	out = append(out, Invariant{
		Name:    "goroutine-count",
		Passed:  final.Goroutines <= goroutineLimit,
		Message: fmt.Sprintf("baseline=%d final=%d limit=%d", baseline.Goroutines, final.Goroutines, goroutineLimit),
	})

	allocLimit := baseline.AllocBytes * 2
	out = append(out, Invariant{
		Name:    "alloc-bytes",
		Passed:  final.AllocBytes <= allocLimit,
		Message: fmt.Sprintf("baseline=%d final=%d limit=%d", baseline.AllocBytes, final.AllocBytes, allocLimit),
	})

	if baseline.FDs > 0 && final.FDs > 0 {
		fdLimit := baseline.FDs + 10
		out = append(out, Invariant{
			Name:    "fd-count",
			Passed:  final.FDs <= fdLimit,
			Message: fmt.Sprintf("baseline=%d final=%d limit=%d", baseline.FDs, final.FDs, fdLimit),
		})
	}

	// GC pressure invariant: total pause time between baseline and
	// final should scale roughly with elapsed time. A 10× ratio means
	// GC is running hot — likely a leak or over-allocating hot path.
	if baseline.NumGC > 0 && final.NumGC > baseline.NumGC {
		elapsed := final.At.Sub(baseline.At).Nanoseconds()
		gcDelta := int64(final.GCPauseTotalNS - baseline.GCPauseTotalNS) //nolint:gosec // pause values are bounded by wall time
		var ratio float64
		if elapsed > 0 {
			ratio = float64(gcDelta) / float64(elapsed)
		}
		// Threshold 5% — GC should not consume more than 5% of elapsed
		// wall time on a healthy daemon.
		gcHealthy := ratio < 0.05
		out = append(out, Invariant{
			Name:    "gc-pressure",
			Passed:  gcHealthy,
			Message: fmt.Sprintf("gc_pause_ratio=%.3f threshold=0.050 (gc_delta=%dns elapsed=%dns)", ratio, gcDelta, elapsed),
		})
	}

	// Session-cache growth: HeapInUse should not grow more than 2×
	// baseline. This is stricter than AllocBytes because it excludes
	// transient allocations.
	if baseline.HeapInUse > 0 {
		heapLimit := baseline.HeapInUse * 2
		out = append(out, Invariant{
			Name:    "heap-inuse",
			Passed:  final.HeapInUse <= heapLimit,
			Message: fmt.Sprintf("baseline=%d final=%d limit=%d", baseline.HeapInUse, final.HeapInUse, heapLimit),
		})
	}

	return out
}
