package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// BenchmarkSpawn_10Tasks measures the per-task overhead of Spawn on a
// small canned workload.
func BenchmarkSpawn_10Tasks(b *testing.B) {
	p := &fakeProvider{name: "bench", text: "ok"}
	tasks := make([]Task, 10)
	for i := range tasks {
		tasks[i] = Task{Prompt: "task", Timeout: 5 * time.Second}
	}
	session := agent.NewSession("bench")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Spawn(context.Background(), session, p, tasks, Policy{MaxConcurrent: 4}, silentLogger()) //nolint:errcheck // bench passthrough
	}
}

// BenchmarkAggregate_100Results measures the aggregator's per-record
// cost on a realistic result set.
func BenchmarkAggregate_100Results(b *testing.B) {
	results := make([]Result, 100)
	for i := range results {
		results[i] = Result{TaskIndex: i, FinalText: "answer", Turns: 1, Duration: 10 * time.Millisecond}
	}
	agg := DefaultAggregator{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agg.Aggregate(results, 32*1024)
	}
}
