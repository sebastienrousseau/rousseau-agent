package ratelimit

import (
	"fmt"
	"sync"
	"testing"
)

// BenchmarkBucket_Allow measures the single-goroutine Allow cost —
// the hot path every rate-limit middleware call goes through.
func BenchmarkBucket_Allow(b *testing.B) {
	bucket := NewBucket(1e9, 1e6) // never denies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bucket.Allow()
	}
}

// BenchmarkKeyed_Allow_Hit measures the hot path when the key is
// already tracked (the common case at steady state).
func BenchmarkKeyed_Allow_Hit(b *testing.B) {
	k := NewKeyedLimiter(1e9, 1e6, 1000)
	k.Allow("hot-key")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k.Allow("hot-key")
	}
}

// BenchmarkKeyed_Allow_ChurningKeys measures the eviction path — a
// malicious sender rotating identifiers hits this. Must not degrade
// significantly.
func BenchmarkKeyed_Allow_ChurningKeys(b *testing.B) {
	k := NewKeyedLimiter(1e9, 1e6, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k.Allow(fmt.Sprintf("k-%d", i))
	}
}

// BenchmarkKeyed_Parallel drives every worker against the same 50-key
// pool. Reveals lock contention.
func BenchmarkKeyed_Parallel(b *testing.B) {
	k := NewKeyedLimiter(1e9, 1e6, 100)
	b.RunParallel(func(pb *testing.PB) {
		var counter int
		var mu sync.Mutex
		for pb.Next() {
			mu.Lock()
			key := fmt.Sprintf("k-%d", counter%50)
			counter++
			mu.Unlock()
			k.Allow(key)
		}
	})
}
