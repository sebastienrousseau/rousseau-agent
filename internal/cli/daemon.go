package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	rcron "github.com/sebastienrousseau/rousseau-agent/internal/cron"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	"github.com/sebastienrousseau/rousseau-agent/internal/ratelimit"
	"github.com/sebastienrousseau/rousseau-agent/internal/resilience"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// daemonWiring bundles the components every long-running-transport
// command (whatsapp, signal, …) shares. Extracted from what used to
// be duplicated blocks in each cobra RunE closure so a new transport
// only needs a Deliver() function to plug in.
type daemonWiring struct {
	Provider    agent.Provider
	Agent       *agent.Agent
	Registry    *tools.Registry
	Router      *transport.Router
	CronStore   *sqlitestore.CronStore
	Sessions    state.Store // the underlying interface
	Concrete    *sqlitestore.Store
	JIDMap      *sqlitestore.JIDMap
	ClaudeCache *sqlitestore.ClaudeSessionCache
	// RateLimiters is a per-transport [ratelimit.KeyedLimiter] map,
	// populated only when the RateLimit config block is non-empty.
	// Transports lookup their entry by name and wrap their Router
	// handler via ratelimit.Wrap before serving.
	RateLimiters map[string]*ratelimit.KeyedLimiter
}

// setUnattendedPermissionDefault forces the claudecli provider into a
// permission mode that lets tool calls complete when the caller has no
// interactive terminal. Emits a WARN so operators see the tradeoff.
func setUnattendedPermissionDefault(opts *Options, transportName string) {
	cfg := opts.Config
	if (cfg.Provider != "" && cfg.Provider != "claudecli") || cfg.ClaudeCLI.PermissionMode != "" {
		return
	}
	cfg.ClaudeCLI.PermissionMode = "bypassPermissions"
	opts.Logger.Warn(transportName+".permission_mode_default",
		"mode", "bypassPermissions",
		"why", "no claudecli.permission_mode set; unattended daemon cannot approve prompts",
		"how_to_override", "set claudecli.permission_mode in ~/.config/rousseau/config.yaml (acceptEdits is a narrower alternative)",
	)
}

// assembleDaemon opens the shared state, wires every agent option, and
// returns the composed pieces ready for a transport to attach a
// Deliver function to the cron scheduler.
//
// Cleanup: the caller is responsible for closing wiring.Sessions and
// shutting down any scheduler it starts.
func assembleDaemon(ctx context.Context, opts *Options, allowlist []string) (*daemonWiring, error) {
	cfg := opts.Config
	provider, err := buildProvider(cfg)
	if err != nil {
		return nil, err
	}

	sessions, err := openStore(ctx, cfg.State.Path)
	if err != nil {
		return nil, err
	}
	concrete := sessions.(*sqlitestore.Store)

	jidMap, err := sqlitestore.NewJIDMap(ctx, concrete)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	claudeCache, err := sqlitestore.NewClaudeSessionCache(ctx, concrete)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	if cc, ok := provider.(*claudecli.Provider); ok {
		cc.WithCache(claudeCache)
	}

	registry := tools.NewRegistry()
	registry.MustRegister(builtin.NewReadTool())
	registry.MustRegister(builtin.NewWriteTool())
	registry.MustRegister(builtin.NewEditTool())
	registry.MustRegister(builtin.NewGrepTool(0, 0))
	registry.MustRegister(builtin.NewBashTool(60 * time.Second))

	// Register every enabled tool-integration suite. Each suite is
	// opt-in via the integrations block in the config; a nil
	// integrations config leaves the registry unchanged.
	intCfg := integrationsFromConfig(cfg)
	if err := integrations.RegisterAll(registry, intCfg, opts.Logger); err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	// Build the per-transport rate-limiter map. Empty ratelimit config
	// leaves the map nil, so the transport callsites simply skip the
	// Wrap when they look up their entry and find nothing.
	rateLimiters, err := buildRateLimiters(cfg.RateLimit)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	approver, err := buildApprover(cfg.Agent.Approver)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	skillsProv, err := buildSkillsProvider(opts)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	ag := agent.New(provider, registry, opts.Logger, agent.Options{
		MaxIterations:  cfg.Agent.MaxIterations,
		SystemPrompt:   systemPrompt(cfg.Agent.SystemPrompt),
		Approver:       approver,
		Compressor:     buildCompressor(cfg.Agent.Compression, provider),
		SkillsProvider: skillsProv,
		RecallProvider: buildRecallProvider(concrete),
	})

	router := transport.NewRouter(ag, sessions, jidMap, opts.Logger, transport.RouterOptions{
		Allowlist: allowlist,
	})

	cronStore, err := sqlitestore.NewCronStore(ctx, concrete)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	return &daemonWiring{
		Provider:     provider,
		Agent:        ag,
		Registry:     registry,
		Router:       router,
		CronStore:    cronStore,
		Sessions:     sessions,
		Concrete:     concrete,
		JIDMap:       jidMap,
		ClaudeCache:  claudeCache,
		RateLimiters: rateLimiters,
	}, nil
}

// TransportHandler returns the transport.Handler each transport
// should attach to its inbound loop. The chain is:
//
//	ratelimit.Wrap  →  resilience.Recover  →  wiring.Router
//
// Rate limiting is only applied when the ratelimit config has a
// non-nil entry for the transport name; Recover always fires so a
// panic in one handler never takes down the whole daemon.
func (w *daemonWiring) TransportHandler(name string, logger *slog.Logger) transport.Handler {
	h := transport.Handler(w.Router)
	h = resilience.Recover(h, name, logger)
	if lim, ok := w.RateLimiters[name]; ok {
		h = ratelimit.Wrap(h, lim, name, "")
	}
	return h
}

// startCron starts a cron scheduler using w.CronStore and the provided
// Delivery. Returned Shutdown func is safe to call multiple times.
func (w *daemonWiring) startCron(ctx context.Context, delivery rcron.Delivery, logger *slog.Logger) (func(), error) {
	scheduler := rcron.New(rcron.Config{
		Store:    w.CronStore,
		Runner:   &rcron.ProviderRunner{Provider: w.Provider},
		Delivery: delivery,
		Logger:   logger,
	})
	if err := scheduler.Start(ctx); err != nil {
		return nil, err
	}
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = scheduler.Shutdown(sctx) //nolint:errcheck // best-effort shutdown
	}
	return shutdown, nil
}
