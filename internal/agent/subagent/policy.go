package subagent

import "time"

// Policy tunes Spawn's execution envelope. Zero-value Policy uses
// the defaults documented on each field.
type Policy struct {
	// MaxConcurrent bounds how many tasks may be in flight at once.
	// Zero uses 4. Values below 1 are treated as 1.
	MaxConcurrent int
	// PerTaskTimeout is the ceiling on a single task's wall-clock
	// duration, applied when Task.Timeout is zero. Zero uses 5 minutes.
	PerTaskTimeout time.Duration
	// BudgetTokens is the total (input+output) token ceiling across
	// every task in the spawn. When zero, no budget is enforced.
	// When positive, tasks that would push the running sum past the
	// budget return early with ErrOverBudget.
	BudgetTokens int
	// AggregatorMaxBytes bounds the combined text length produced by
	// Aggregate. Zero uses 32 KiB. Preventing an unbounded aggregate
	// blob is what keeps a single "spawn 10 tasks" turn from blowing
	// the parent's context window.
	AggregatorMaxBytes int
}

func (p Policy) maxConcurrent() int {
	if p.MaxConcurrent <= 0 {
		return 4
	}
	return p.MaxConcurrent
}

func (p Policy) perTaskTimeout() time.Duration {
	if p.PerTaskTimeout <= 0 {
		return 5 * time.Minute
	}
	return p.PerTaskTimeout
}

// EffectiveMaxConcurrent is the concurrency limit Spawn applies for
// this policy, defaults resolved. Callers that let a model request a
// lower limit compare against this, not the raw field.
func (p Policy) EffectiveMaxConcurrent() int { return p.maxConcurrent() }

// EffectivePerTaskTimeout is the per-task timeout Spawn applies for
// this policy, defaults resolved.
func (p Policy) EffectivePerTaskTimeout() time.Duration { return p.perTaskTimeout() }

func (p Policy) aggregatorMax() int {
	if p.AggregatorMaxBytes <= 0 {
		return 32 * 1024
	}
	return p.AggregatorMaxBytes
}
