package subagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// ErrOverBudget is returned by a task's Result.Err when the aggregate
// token budget was exhausted before it could run.
var ErrOverBudget = errors.New("subagent: over budget")

// ErrNoTasks is returned by Spawn when tasks is empty.
var ErrNoTasks = errors.New("subagent: no tasks")

// Spawn runs every task against a detached copy of parent, honouring
// policy. Results are returned in the same order as tasks so callers
// can correlate without a map.
//
// Spawn blocks until every task completes or ctx cancels. Cancellation
// propagates to every in-flight sub-agent; already-completed results
// are returned as-is.
//
// Detached-copy semantics: each Task receives its own Session cloned
// from parent's message history at Spawn time. Mutations to the copy
// during execution don't touch the parent — this is why Spawn is safe
// to invoke concurrently on the same parent Session.
func Spawn(ctx context.Context, parent *agent.Session, provider agent.Provider, tasks []Task, policy Policy, logger *slog.Logger) ([]Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	if parent == nil {
		return nil, errors.New("subagent: nil parent session")
	}
	if provider == nil {
		return nil, errors.New("subagent: nil provider")
	}
	if logger == nil {
		logger = slog.Default()
	}

	sem := newSemaphore(policy.maxConcurrent())

	var mu sync.Mutex
	tokensSoFar := 0
	results := make([]Result, len(tasks))

	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()

			start := time.Now()
			r := Result{TaskIndex: i}

			// Concurrency gate. If ctx cancels before we acquire, exit
			// with ctx.Err() before touching the provider.
			if err := sem.acquire(ctx); err != nil {
				r.Err = err
				r.Duration = time.Since(start)
				mu.Lock()
				results[i] = r
				mu.Unlock()
				return
			}
			defer sem.release()

			// Budget check under lock so several goroutines don't all
			// pass the check on a low-remaining budget.
			mu.Lock()
			over := policy.BudgetTokens > 0 && tokensSoFar >= policy.BudgetTokens
			mu.Unlock()
			if over {
				r.Err = ErrOverBudget
				r.Duration = time.Since(start)
				mu.Lock()
				results[i] = r
				mu.Unlock()
				return
			}

			// Per-task timeout wraps the caller's ctx.
			timeout := t.Timeout
			if timeout <= 0 {
				timeout = policy.perTaskTimeout()
			}
			taskCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// Choose provider for this task.
			p := provider
			if t.ProviderOverride != nil {
				p = t.ProviderOverride
			}

			observability.SubagentSpawned.WithLabelValues(p.Name()).Inc()

			out := runOne(taskCtx, parent, p, t)
			out.TaskIndex = i
			out.Duration = time.Since(start)

			// Update running budget.
			mu.Lock()
			tokensSoFar += out.TokensIn + out.TokensOut
			results[i] = out
			mu.Unlock()

			logger.Info("subagent.completed",
				slog.Int("task_index", i),
				slog.Int("turns", out.Turns),
				slog.Int("tokens_in", out.TokensIn),
				slog.Int("tokens_out", out.TokensOut),
				slog.Duration("duration", out.Duration),
				slog.Any("err", out.Err),
			)
		}()
	}
	wg.Wait()
	return results, nil
}

// runOne executes a single sub-agent's turn loop. It drives the
// provider for at most MaxTurns cycles, appending the assistant's
// reply after each. Terminates on end_turn / max_tokens / provider
// error / ctx cancel.
func runOne(ctx context.Context, parent *agent.Session, provider agent.Provider, t Task) Result {
	maxTurns := t.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 4
	}

	// Detached copy of parent history.
	messages := make([]agent.Message, 0, len(parent.Messages)+1)
	messages = append(messages, parent.Messages...)
	if t.Prompt != "" {
		messages = append(messages, agent.NewUserText(t.Prompt))
	}

	system := t.System

	res := Result{}
	for turn := 0; turn < maxTurns; turn++ {
		select {
		case <-ctx.Done():
			res.Err = ctx.Err()
			return res
		default:
		}

		req := agent.Request{
			SessionID: parent.ID,
			System:    system,
			Messages:  messages,
		}
		resp, err := provider.Complete(ctx, req)
		if err != nil {
			res.Err = fmt.Errorf("subagent: provider: %w", err)
			return res
		}
		res.Turns++
		res.TokensIn += resp.Usage.InputTokens
		res.TokensOut += resp.Usage.OutputTokens

		// Track the last assistant text seen.
		if txt := firstText(resp.Message.Content); txt != "" {
			res.FinalText = txt
		}
		messages = append(messages, resp.Message)

		switch resp.StopReason {
		case agent.StopEndTurn, agent.StopMaxTokens:
			return res
		}
		// tool_use / other → continue driving the loop. Sub-agents that
		// need tool execution get it via provider.Complete's internal
		// handling (claudecli's built-in tools; the anthropic /
		// bedrock / vertex tool loop is caller-side today, so a Task
		// that lands StopToolUse on those providers will terminate on
		// the next iteration when the loop bounds hit).
	}
	return res
}

func firstText(cs []agent.Content) string {
	for _, c := range cs {
		if c.Kind == agent.ContentText && c.Text != "" {
			return c.Text
		}
	}
	return ""
}
