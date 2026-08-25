package cli

import (
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// buildHooks translates config.HooksConfig into a [hooks.Runner]
// suitable for [agent.Options.Hooks]. Returns nil when no hooks are
// declared — the agent loop's nil-check then short-circuits and
// doesn't spend any cycles on hook plumbing.
func buildHooks(cfg config.HooksConfig, logger *slog.Logger) hooks.Runner {
	byEvent := map[hooks.Event][]hooks.Config{}
	appendEvent := func(e hooks.Event, in []config.HookConfig) {
		if len(in) == 0 {
			return
		}
		out := make([]hooks.Config, 0, len(in))
		for _, h := range in {
			out = append(out, hooks.Config{
				Name:    h.Name,
				Command: h.Command,
				Args:    h.Args,
				Env:     h.Env,
				Timeout: time.Duration(h.TimeoutSeconds) * time.Second,
			})
		}
		byEvent[e] = out
	}
	appendEvent(hooks.EventPreToolUse, cfg.PreToolUse)
	appendEvent(hooks.EventPostToolUse, cfg.PostToolUse)
	appendEvent(hooks.EventPreTurn, cfg.PreTurn)
	appendEvent(hooks.EventPostTurn, cfg.PostTurn)
	appendEvent(hooks.EventOnError, cfg.OnError)

	if len(byEvent) == 0 {
		return nil
	}
	return hooks.New(byEvent, logger)
}
