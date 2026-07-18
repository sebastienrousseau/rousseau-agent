// Package subagent re-exports the internal/agent/subagent surface so
// external modules can use Spawn without importing /internal.
package subagent

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
)

// Task aliases [subagent.Task].
type Task = subagent.Task

// Result aliases [subagent.Result].
type Result = subagent.Result

// Policy aliases [subagent.Policy].
type Policy = subagent.Policy

// Aggregator aliases [subagent.Aggregator].
type Aggregator = subagent.Aggregator

// DefaultAggregator aliases [subagent.DefaultAggregator].
type DefaultAggregator = subagent.DefaultAggregator

// Spawn is a direct alias for [subagent.Spawn].
var Spawn = subagent.Spawn

// AggregateJSON is a direct alias for [subagent.AggregateJSON].
var AggregateJSON = subagent.AggregateJSON

// Sentinels.
var (
	ErrOverBudget = subagent.ErrOverBudget
	ErrNoTasks    = subagent.ErrNoTasks
)
