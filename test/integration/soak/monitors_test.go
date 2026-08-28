package soak

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitor_CollectsSamples(t *testing.T) {
	m := NewMonitor(10 * time.Millisecond)
	m.Start()
	time.Sleep(50 * time.Millisecond)
	samples := m.Stop()
	assert.GreaterOrEqual(t, len(samples), 2)
	for _, s := range samples {
		assert.Positive(t, s.Goroutines)
		assert.Positive(t, s.SysBytes)
	}
}

func TestMonitor_ZeroIntervalDefaults(t *testing.T) {
	m := NewMonitor(0)
	assert.Equal(t, 5*time.Minute, m.tick)
}

func TestCheckInvariants_EmptySamplesFails(t *testing.T) {
	invs := CheckInvariants(nil, 5*time.Minute)
	require.Len(t, invs, 1)
	assert.False(t, invs[0].Passed)
}

func TestCheckInvariants_PassOnStableRun(t *testing.T) {
	origin := time.Now()
	samples := []Sample{
		{At: origin, Goroutines: 10, AllocBytes: 1000, FDs: 5},
		{At: origin.Add(1 * time.Minute), Goroutines: 11, AllocBytes: 1200, FDs: 5},
		{At: origin.Add(5 * time.Minute), Goroutines: 11, AllocBytes: 1500, FDs: 6},
	}
	invs := CheckInvariants(samples, 30*time.Second)
	for _, inv := range invs {
		assert.True(t, inv.Passed, "%s: %s", inv.Name, inv.Message)
	}
}

func TestCheckInvariants_FailsOnGoroutineLeak(t *testing.T) {
	origin := time.Now()
	samples := []Sample{
		{At: origin, Goroutines: 10, AllocBytes: 1000},
		{At: origin.Add(31 * time.Second), Goroutines: 11, AllocBytes: 1050},
		{At: origin.Add(2 * time.Minute), Goroutines: 100, AllocBytes: 1200},
	}
	invs := CheckInvariants(samples, 30*time.Second)
	found := false
	for _, inv := range invs {
		if inv.Name == "goroutine-count" && !inv.Passed {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCheckInvariants_FailsOnAllocLeak(t *testing.T) {
	origin := time.Now()
	// Baseline is picked at the first sample ≥ baselineAfter (30s
	// here). Steady memory around 1000 through 30s; then a leak.
	samples := []Sample{
		{At: origin, Goroutines: 10, AllocBytes: 1000},
		{At: origin.Add(31 * time.Second), Goroutines: 10, AllocBytes: 1050},
		{At: origin.Add(2 * time.Minute), Goroutines: 10, AllocBytes: 5000},
	}
	invs := CheckInvariants(samples, 30*time.Second)
	found := false
	for _, inv := range invs {
		if inv.Name == "alloc-bytes" && !inv.Passed {
			found = true
		}
	}
	assert.True(t, found)
}

func TestReport_RendersMarkdown(t *testing.T) {
	origin := time.Now()
	samples := []Sample{
		{At: origin, Goroutines: 10, AllocBytes: 1024, SysBytes: 2048, HeapInUse: 512, FDs: 4},
	}
	invariants := []Invariant{
		{Name: "test-inv", Passed: true, Message: "ok"},
	}
	report := Report(5*time.Minute, samples, invariants, []string{"note-1"})
	assert.Contains(t, report, "# rousseau-agent soak report")
	assert.Contains(t, report, "test-inv")
	assert.Contains(t, report, "note-1")
	assert.Contains(t, report, "Elapsed")
}

func TestAllPassed(t *testing.T) {
	assert.True(t, AllPassed([]Invariant{{Passed: true}, {Passed: true}}))
	assert.False(t, AllPassed([]Invariant{{Passed: true}, {Passed: false}}))
	assert.True(t, AllPassed(nil))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512B", humanBytes(512))
	assert.True(t, strings.Contains(humanBytes(2048), "K"))
	assert.True(t, strings.Contains(humanBytes(2*1024*1024), "M"))
}

func TestParseDurationEnv(t *testing.T) {
	t.Setenv("SOAK_TEST_DUR", "10s")
	assert.Equal(t, 10*time.Second, parseDurationEnv("SOAK_TEST_DUR", time.Minute))
	t.Setenv("SOAK_TEST_DUR", "30")
	assert.Equal(t, 30*time.Second, parseDurationEnv("SOAK_TEST_DUR", time.Minute))
	t.Setenv("SOAK_TEST_DUR", "garbage")
	assert.Equal(t, time.Minute, parseDurationEnv("SOAK_TEST_DUR", time.Minute))
	t.Setenv("SOAK_TEST_DUR", "")
	assert.Equal(t, time.Minute, parseDurationEnv("SOAK_TEST_DUR", time.Minute))
}

func TestPickSampleInterval(t *testing.T) {
	assert.Equal(t, 200*time.Millisecond, pickSampleInterval(5*time.Second))
	assert.Equal(t, 5*time.Minute, pickSampleInterval(24*time.Hour))
}
