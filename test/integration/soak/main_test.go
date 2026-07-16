package soak

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestSoak drives a synthetic workload against a fake provider while
// [Monitor] samples runtime + FD counts. Duration is controlled by
// SOAK_DURATION (default 30s — smoke). PR CI runs 30m; nightly runs
// 24h.
//
// Failure modes checked:
//   - goroutine count leaked past 1.2× baseline
//   - alloc bytes past 2× baseline
//   - FD count past baseline + 10 (Linux only)
//
// A markdown summary is written to SOAK_REPORT (default
// /tmp/soak-report.md) so CI can upload it as an artefact.
func TestSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak: skipped under -short")
	}
	dur := parseDurationEnv("SOAK_DURATION", 30*time.Second)
	sampleEvery := parseDurationEnv("SOAK_SAMPLE_INTERVAL", pickSampleInterval(dur))
	rate := parseDurationEnv("SOAK_MESSAGE_INTERVAL", 500*time.Millisecond)
	reportPath := os.Getenv("SOAK_REPORT")
	if reportPath == "" {
		reportPath = "/tmp/soak-report.md"
	}

	t.Logf("soak: duration=%s sample=%s rate=%s", dur, sampleEvery, rate)

	// The mock provider echoes a canned string with a fixed usage
	// count; every task increments a Complete counter.
	p := &countingProvider{name: "soak-fake", text: "ok"}
	session := agent.NewSession("soak")

	monitor := NewMonitor(sampleEvery)
	monitor.Start()

	// Baseline "warm-up" — a handful of turns before the monitor
	// establishes its 30-minute baseline point. Cheap; doesn't affect
	// samples.
	warmUp(t, p, session, 4)

	// Drive the message-per-tick loop until dur elapses or ctx cancels.
	driverCtx, cancelDriver := context.WithTimeout(context.Background(), dur)
	defer cancelDriver()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		driveLoop(driverCtx, p, session, rate)
	}()
	wg.Wait()

	samples := monitor.Stop()
	baselineAfter := pickBaselineAfter(dur)
	invariants := CheckInvariants(samples, baselineAfter)

	notes := []string{
		fmt.Sprintf("provider calls: %d", p.calls.Load()),
		fmt.Sprintf("session messages: %d", len(session.Messages)),
		fmt.Sprintf("baseline sampled after: %s", baselineAfter),
		fmt.Sprintf("runtime.NumCPU: %d", runtime.NumCPU()),
	}
	report := Report(dur, samples, invariants, notes)
	if err := WriteReport(reportPath, report); err != nil {
		t.Logf("soak: WriteReport failed: %v", err)
	}

	for _, inv := range invariants {
		if !inv.Passed {
			t.Errorf("invariant %q failed: %s", inv.Name, inv.Message)
		}
	}
}

// warmUp drives a small number of turns before the invariant baseline
// so the sample at 30 minutes reflects steady state, not cold-start.
func warmUp(t *testing.T, p agent.Provider, s *agent.Session, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		s.Append(agent.NewUserText("warm-up"))
		resp, err := p.Complete(context.Background(), agent.Request{Messages: s.Messages})
		if err != nil {
			t.Fatalf("warm-up complete: %v", err)
		}
		s.Append(resp.Message)
	}
}

// driveLoop appends a user message + provider response every rate
// until ctx cancels.
func driveLoop(ctx context.Context, p agent.Provider, s *agent.Session, rate time.Duration) {
	t := time.NewTicker(rate)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Append(agent.NewUserText("drive"))
			resp, err := p.Complete(ctx, agent.Request{Messages: s.Messages})
			if err != nil {
				return
			}
			s.Append(resp.Message)
			// Bounded history so we don't OOM by growing indefinitely.
			if len(s.Messages) > 200 {
				s.Messages = s.Messages[len(s.Messages)-200:]
			}
		}
	}
}

// countingProvider is a canned agent.Provider for the soak loop.
type countingProvider struct {
	name  string
	text  string
	calls atomic.Int64
}

func (c *countingProvider) Name() string { return c.name }

func (c *countingProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	c.calls.Add(1)
	return agent.Response{
		Message:    agent.NewAssistantText(c.text),
		StopReason: agent.StopEndTurn,
		Usage:      agent.Usage{InputTokens: 5, OutputTokens: 3},
	}, nil
}

// pickSampleInterval keeps the sample series manageable: aim for
// ~50 samples over the run.
func pickSampleInterval(dur time.Duration) time.Duration {
	target := dur / 50
	if target < 200*time.Millisecond {
		return 200 * time.Millisecond
	}
	if target > 5*time.Minute {
		return 5 * time.Minute
	}
	return target
}

// pickBaselineAfter matches the run length: for very short runs the
// baseline is at 10% of the duration; for long runs it's 30 minutes.
func pickBaselineAfter(dur time.Duration) time.Duration {
	base := dur / 10
	if base > 30*time.Minute {
		return 30 * time.Minute
	}
	if base < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	return base
}

func parseDurationEnv(key string, dflt time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return dflt
	}
	// Accept integer seconds for CI convenience.
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return dflt
	}
	return d
}
