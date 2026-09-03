package cli

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/control"
	rcron "github.com/sebastienrousseau/rousseau-agent/internal/cron"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	mcpclient "github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
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
	// Progress is the shared progress-event bus. Every transport-side
	// Supervisor uses it as its Registry.Bus, so the agent's per-turn
	// progress events (tool_use, text_delta) flow back to the
	// transport's live-update reporter. The agent's Options.Progress
	// is set to the same bus for calls that skip the Supervisor
	// (cron, tests) — either half alone is enough to make events
	// arrive.
	Progress *progress.Bus

	Provider    agent.Provider
	Agent       *agent.Agent
	Registry    *tools.Registry
	Router      *transport.Router
	CronStore   *sqlitestore.CronStore
	Sessions    state.Store // the underlying interface
	Concrete    *sqlitestore.Store
	JIDMap      *sqlitestore.JIDMap
	ClaudeCache *sqlitestore.ClaudeSessionCache
	CostStore   *sqlitestore.SessionCostStore
	// Identities is the cross-transport identity resolver. Wired
	// into every per-transport router via routerFor so /whoami,
	// /link, /unlink and the SSO-identity stash all work. Not the
	// same as SSOBindings — Identities owns the "one person, many
	// handles" graph; SSOBindings owns "handle → verified OIDC
	// subject". A /login binding uses both.
	Identities *sqlitestore.IdentityStore

	// routerOpts snapshots the RouterOptions surface used by
	// assembleDaemon so routerFor can rebuild per-transport
	// routers with identical config. Kept as a single struct
	// rather than nine separate fields to make new options
	// automatic in the per-transport routers too.
	routerOpts transport.RouterOptions
	// Licence is the loaded [license.Checker] the daemon consults
	// at every gated code path. Never nil — falls back to
	// [license.Core] when unlicensed.
	Licence license.Checker
	// SSOBindings is the on-disk store of /login-verified
	// identities. Populated even when SSO isn't licensed (the
	// [sso.NoBindings] fallback) so downstream call sites don't
	// nil-check.
	SSOBindings sso.BindingStore
	// SCIMServer is the (optional) SCIM 2.0 HTTP Service
	// Provider. Nil when the operator hasn't configured
	// auth.sso.scim.addr OR the licence doesn't unlock
	// FeatureSSO. When non-nil, callers start it via
	// SCIMServer.ListenAndServe from a background goroutine.
	SCIMServer *scim.Server
	// SCIMAddr is the bind address the SCIM server listens on
	// (mirrors auth.sso.scim.addr). Empty when SCIMServer is
	// nil. Kept on the wiring so a transport-runner can spawn
	// the ListenAndServe goroutine with the same lifetime as
	// itself.
	SCIMAddr string
	// AuditSink is the enterprise audit-egress sink; downstream
	// callers (agent tool-call instrumentation, SSO
	// login/logout, license state changes) Emit into it. Never
	// nil — [audit_egress.Nop] is the shipped fallback so call
	// sites don't nil-check. Wrapped in [audit_egress.ChainedSink]
	// when the operator sets observability.audit_egress.chained.
	AuditSink audit_egress.Sink
	// MCPClients holds every started MCP client subprocess. Callers
	// close them (via wiring.Cleanup) on shutdown so the subprocesses
	// exit cleanly rather than being reaped when the parent dies.
	MCPClients []*mcpclient.Client
	// RateLimiters is a per-transport [ratelimit.KeyedLimiter] map,
	// populated only when the RateLimit config block is non-empty.
	// Transports lookup their entry by name and wrap their Router
	// handler via ratelimit.Wrap before serving.
	RateLimiters map[string]*ratelimit.KeyedLimiter
	// Logger is retained so Cleanup can log MCP-client-close errors
	// on the same logger the daemon used at startup.
	Logger *slog.Logger

	// routers holds one transport.Router per transport name.
	// Lazily populated on first routerFor call. Each per-transport
	// router carries its Transport name so identity + SSO lookups
	// key by (transport, sender) — a bare shared router would
	// collide handles that happen to be equal across transports
	// (e.g. a Slack user "U12345" and a Signal number that happens
	// to render the same). Kept behind the same mutex as the
	// supervisor cache since both are built on the same "first
	// use of transport X" trigger.
	routers map[string]*transport.Router
	// supervisors holds one transport.Supervisor per transport name.
	// Lazily populated on first TransportHandler call so a transport
	// that does not run in this process pays no allocation. Each
	// Supervisor owns its own control.Registry, which keeps
	// conversations on different transports from colliding on the same
	// key (e.g. a phone number that reaches both signal and whatsapp).
	supervisorMu sync.Mutex
	supervisors  map[string]*transport.Supervisor
}

// StartBackgroundServers launches any long-running HTTP
// servers wired into daemonWiring (currently: SCIM). Each
// server runs in its own goroutine and shuts down when ctx is
// cancelled. Errors are logged via wiring.Logger but not
// returned — one server failing must not take the transport
// offline.
//
// Callers (every transport RunE) invoke this once, right after
// assembleDaemon, before entering their inbound loop. Idempotent
// on nil sub-servers; skips ones the operator didn't configure.
func (w *daemonWiring) StartBackgroundServers(ctx context.Context) {
	if w.SCIMServer != nil && w.SCIMAddr != "" {
		go func() {
			if err := w.SCIMServer.ListenAndServe(ctx, w.SCIMAddr); err != nil {
				w.Logger.Warn("scim.serve_failed",
					slog.String("addr", w.SCIMAddr),
					slog.String("err", err.Error()),
				)
			}
		}()
	}
}

// Cleanup releases every resource held by the wiring: closes the MCP
// client subprocesses (in reverse start order), then closes the
// underlying state Store. Safe to defer. Errors from MCP client Close
// calls are logged but not returned — shutdown is best-effort.
//
// Callers that predate this helper still work: they may call
// wiring.Sessions.Close() directly. The tradeoff is that MCP client
// subprocesses leak until the parent daemon exits (the OS reaps them
// then, so the leak is bounded).
func (w *daemonWiring) Cleanup() error {
	closeMCPClients(w.MCPClients, w.Logger)
	// Drain the audit sink first — losing the shutdown record
	// (daemon.stop) after Sessions.Close blocks would surprise
	// operators reviewing SIEM histories. Bounded by a small
	// context so a wedged remote SIEM can't stall daemon
	// shutdown.
	if w.AuditSink != nil {
		_ = w.AuditSink.Emit(context.Background(), audit_egress.Record{ //nolint:errcheck // best-effort shutdown breadcrumb; sink counters are authoritative
			Category: "daemon", Actor: "rousseau",
			Verb: "stop", Object: "daemon", Result: "success",
		})
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = w.AuditSink.Close(closeCtx) //nolint:errcheck // Close returns ctx err only; already bounded above
		cancel()
	}
	if w.Sessions != nil {
		return w.Sessions.Close()
	}
	return nil
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

	concrete, err := openSQLiteStore(ctx, cfg.State)
	if err != nil {
		return nil, err
	}
	sessions := state.Store(concrete)

	identities, err := sqlitestore.NewIdentityStore(ctx, concrete)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

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
	costStore, err := sqlitestore.NewSessionCostStore(ctx, concrete)
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
	bash, err := buildBashTool(opts.Config.Tools.Bash)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, fmt.Errorf("cli: build bash tool: %w", err)
	}
	registry.MustRegister(bash)
	// spawn_subagent exposes the sub-agent parallelism primitive
	// (subagent.Spawn) to the model. Zero-value Policy uses the
	// defaults documented on subagent.Policy (MaxConcurrent=4,
	// PerTaskTimeout=5m, no aggregate token budget). Operators wanting
	// tighter limits can pass a non-zero Policy here.
	registry.MustRegister(builtin.NewSpawnSubagentTool(subagent.Policy{}))

	// Register every enabled tool-integration suite. Each suite is
	// opt-in via the integrations block in the config; a nil
	// integrations config leaves the registry unchanged.
	intCfg := integrationsFromConfig(cfg)
	if err := integrations.RegisterAll(registry, intCfg, opts.Logger); err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	// Spawn every configured MCP client and register the tools each
	// server advertises. Fail-soft per client: a broken MCP server
	// logs a WARN and is skipped rather than taking the daemon down.
	// Empty mcp.clients config leaves mcpClients nil.
	mcpClients, mcpToolNames, err := startMCPClients(ctx, cfg.MCP, registry, opts.Logger)
	if err != nil {
		closeMCPClients(mcpClients, opts.Logger)
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	if len(mcpToolNames) > 0 {
		opts.Logger.Info("mcp.clients.ready",
			slog.Int("server_count", len(mcpClients)),
			slog.Int("tool_count", len(mcpToolNames)),
		)
	}

	// Build the per-transport rate-limiter map. Empty ratelimit config
	// leaves the map nil, so the transport callsites simply skip the
	// Wrap when they look up their entry and find nothing.
	rateLimiters, err := buildRateLimiters(cfg.RateLimit)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	// Load the licence once — the approver-RBAC wrap AND the
	// SSO surface below both consult it, and doctor reads from
	// the same checker (a single load means one consistent
	// tier picture regardless of which entry point is used).
	checker := license.Load(license.Source{}, opts.Logger)

	approver, err := buildApprover(cfg.Agent.Approver)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	// Layer RBAC on top when the operator configured rules AND
	// the licence unlocks governance-advanced. See wrapWithRBAC
	// for the fail-safe behaviour on partial configuration.
	approver = wrapWithRBAC(approver, cfg.Agent.Approver.RBAC, checker, opts.Logger)
	// Layer OPA on top of RBAC — a request must pass BOTH
	// governance layers before reaching the mode-selected
	// (pattern / TUI) approver. Same three-condition fail-safe
	// gate as wrapWithRBAC.
	approver = wrapWithOPA(ctx, approver, cfg.Agent.Approver.OPA, checker, opts.Logger)

	skillsProv, err := buildSkillsProvider(opts, checker)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}
	progressBus := progress.NewBus(progress.BusOptions{})

	// Build the audit-egress sink BEFORE the multi-party
	// wrapper (which feeds pending-approval lifecycle events
	// through it via an adapter). Sink is always non-nil (Nop
	// when off/unlicensed) so downstream Emit call sites don't
	// need nil checks.
	//
	// A "daemon.start" record is stamped once the wiring is
	// complete, giving operators a zero-code way to verify
	// their SIEM pipeline works.
	//
	// The chain-state store is only opened when chained=true.
	// Its schema-apply is idempotent so a subsequent restart
	// without chained=true leaves the row untouched (harmless).
	var chainStore audit_egress.ChainStore
	if cfg.Observability.AuditEgress.Chained {
		cs, csErr := sqlitestore.NewAuditChainState(ctx, concrete)
		if csErr != nil {
			_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
			return nil, fmt.Errorf("cli: audit chain state: %w", csErr)
		}
		chainStore = cs
	}
	auditSink := buildAuditSink(cfg.Observability.AuditEgress, checker, chainStore, opts.Logger)

	// Snapshot the licence state into the audit trail so the
	// SIEM has a boot-time record of "what tier is this daemon
	// running as, and when does it expire". Standard SOC 2
	// Change-Management visibility — a compliance reviewer can
	// filter for Category=license across a fleet to answer
	// "were any daemons running unlicensed this quarter?".
	emitLicenseSnapshot(auditSink, checker)

	// Layer multi-party approval as the OUTERMOST governance
	// wrapper — a request must pass MultiParty → OPA → RBAC →
	// inner. Composition intent: N-approvers is the highest-
	// friction gate; do it first so a group / Rego / pattern
	// deny short-circuits the ops-heavy step. Returns a nil
	// PendingManager when the wrap didn't take effect (no rules
	// or unlicensed); router thread-safe on nil.
	var pendingApprovals *approval.PendingManager
	approver, pendingApprovals = wrapWithMultiParty(
		approver, cfg.Agent.Approver.MultiParty, checker,
		newApprovalAuditAdapter(auditSink), opts.Logger,
	)

	ag := agent.New(provider, registry, opts.Logger, agent.Options{
		MaxIterations:  cfg.Agent.MaxIterations,
		SystemPrompt:   systemPrompt(cfg.Agent.SystemPrompt),
		Approver:       approver,
		Compressor:     buildCompressor(cfg.Agent.Compression, provider),
		SkillsProvider: skillsProv,
		RecallProvider: buildRecallProvider(concrete),
		CostRecorder:   sqlitestore.NewCostRecorder(costStore, nil),
		Hooks:          buildHooks(cfg.Hooks, opts.Logger),
		Progress:       progressBus,
		AuditSink:      auditSink,
	})

	// Build the optional SCIM Service Provider — pull-based
	// directory sync from the IdP. Silent when unconfigured
	// or unlicensed; matches the same three-condition gate
	// pattern as the other governance surfaces. Doctor
	// surfaces both cases so the operator sees "you set an
	// addr but the licence doesn't unlock it".
	scimServer, scimAddr, err := buildSCIM(ctx, cfg.Auth.SSO.SCIM, checker, concrete, opts.Logger)
	if err != nil {
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	// Assemble the SSO surface (Directory + BindingStore) using
	// the licence loaded above. The router below consults both
	// on every inbound message; doctor rows read from the same
	// checker.
	ssoDir, ssoStore, err := buildSSO(ctx, cfg.Auth.SSO, checker, concrete, opts.Logger)
	if err != nil {
		closeMCPClients(mcpClients, opts.Logger)
		_ = sessions.Close() //nolint:errcheck // constructor rollback; primary error is being returned
		return nil, err
	}

	// routerOpts is the shared option surface. routerFor uses it
	// verbatim per-transport, overriding only Identity+Transport.
	// The base w.Router below is kept for backwards compatibility
	// with callers that reach in via wiring.Router directly (e.g.
	// daemon_test) — TransportHandler goes through routerFor.
	routerOpts := transport.RouterOptions{
		Allowlist:     allowlist,
		SSO:           ssoDir,
		SSOStore:      ssoStore,
		SSOBindingTTL: cfg.Auth.SSO.BindingTTL,
		AuditSink:     auditSink,
		Approvals:     pendingApprovals,
		BuildStamp:    fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
	}
	router := transport.NewRouter(ag, sessions, jidMap, opts.Logger, routerOpts)

	cronStore, err := sqlitestore.NewCronStore(ctx, concrete)
	if err != nil {
		closeMCPClients(mcpClients, opts.Logger)
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
		CostStore:    costStore,
		Identities:   identities,
		routerOpts:   routerOpts,
		MCPClients:   mcpClients,
		RateLimiters: rateLimiters,
		Logger:       opts.Logger,
		Progress:     progressBus,
		Licence:      checker,
		SSOBindings:  ssoStore,
		AuditSink:    auditSink,
		SCIMServer:   scimServer,
		SCIMAddr:     scimAddr,
	}, nil
}

// buildSCIM composes the optional SCIM 2.0 SP. Returns (nil,
// "", nil) when the operator hasn't set auth.sso.scim.addr,
// mirroring the three-condition fail-safe pattern:
//
//   - No addr configured → nothing to do.
//   - Addr set but no licence → INFO log + return nil so the
//     doctor row surfaces the "your SCIM is inert" case
//     without side-effects.
//   - Addr set + licensed but bearer_token empty → WARN log +
//     return nil (SCIM has no anonymous mode; refuse to boot
//     an open endpoint).
//
// On success returns a wired *scim.Server + the addr so the
// transport runner can spawn ListenAndServe with its own
// context.
func buildSCIM(ctx context.Context, cfg config.SCIMConfig, checker license.Checker, store *sqlitestore.Store, logger *slog.Logger) (*scim.Server, string, error) {
	if cfg.Addr == "" {
		return nil, "", nil
	}
	if checker == nil || !checker.IsEnabled(license.FeatureSSO) {
		logger.Info("scim.licence_required",
			slog.String("addr", cfg.Addr),
			slog.String("feature", string(license.FeatureSSO)),
			slog.String("hint", "add ROUSSEAU_LICENSE_KEY with sso to activate; see docs/COMMERCIAL.md"),
		)
		return nil, "", nil
	}
	if cfg.BearerToken == "" {
		logger.Warn("scim.no_bearer_token",
			slog.String("addr", cfg.Addr),
			slog.String("hint", "SCIM refuses anonymous — set auth.sso.scim.bearer_token from a secret manager"),
		)
		return nil, "", nil
	}
	scimStore, err := sqlitestore.NewSCIMStore(ctx, store)
	if err != nil {
		return nil, "", fmt.Errorf("cli: scim store: %w", err)
	}
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       scimStore,
		BearerToken: cfg.BearerToken,
		BaseURL:     cfg.BaseURL,
		Logger:      logger,
	})
	if err != nil {
		return nil, "", fmt.Errorf("cli: scim server: %w", err)
	}
	return srv, cfg.Addr, nil
}

// buildAuditSink constructs the enterprise audit-egress sink
// from cfg + licence + logger. Always non-nil — returns
// [audit_egress.Nop] on the three documented no-op paths:
// unconfigured / licence doesn't unlock / bad config. On the
// happy path, optionally wraps the underlying sink in a
// [audit_egress.ChainedSink] when cfg.Chained is true — the
// tamper-evident wire shape SIEMs pin to.
//
// A "daemon.start" record is stamped as the first Emit so
// operators can verify their pipeline works end-to-end without
// waiting for real activity.
func buildAuditSink(cfg config.AuditEgressConfig, checker license.Checker, chainStore audit_egress.ChainStore, logger *slog.Logger) audit_egress.Sink {
	inner := audit_egress.New(audit_egress.Config{
		Kind:          audit_egress.Kind(cfg.Kind),
		Endpoint:      cfg.Endpoint,
		Headers:       cfg.Headers,
		BatchSize:     cfg.BatchSize,
		FlushInterval: cfg.FlushInterval,
		QueueSize:     cfg.QueueSize,
		HTTPTimeout:   cfg.HTTPTimeout,
	}, checker, logger)
	// Nop → no chain wrap. Wrapping a Nop would waste hash
	// computation on records that never leave the process.
	if _, isNop := inner.(audit_egress.Nop); isNop {
		return inner
	}
	sink := inner
	if cfg.Chained {
		opts := []audit_egress.ChainOption{audit_egress.WithChainLogger(logger)}
		if chainStore != nil {
			opts = append(opts, audit_egress.WithChainStore(chainStore))
		}
		sink = audit_egress.NewChainedSink(inner, opts...)
	}
	// Boot event — best-effort. A failed Emit here is not a
	// daemon-startup failure; the sink's own dropped-record
	// counter (or the Nop return) tells the operator.
	_ = sink.Emit(context.Background(), audit_egress.Record{ //nolint:errcheck // best-effort boot breadcrumb; sink counters are authoritative
		Category: "daemon",
		Actor:    "rousseau",
		Verb:     "start",
		Object:   "daemon",
		Result:   "success",
	})
	return sink
}

// emitLicenseSnapshot writes one Category=license record to sink
// describing the loaded [license.Checker]'s state. Detail carries
// the operator-actionable fields ([license.Info.Subject],
// features, expiry, expiring flag) so SIEM dashboards can slice
// by tier + expiry-warning window without joining against a
// separate source.
//
// Result values:
//
//   - "core"     — no licence configured (OSS install; expected)
//   - "invalid"  — configured but signature / structure rejected
//   - "expiring" — valid but inside [license.ExpiryWarnWindow]
//   - "active"   — valid + not expiring
//
// Best-effort — a nil sink, a Nop sink, or a wedged remote all
// swallow silently. The daemon boots regardless.
func emitLicenseSnapshot(sink audit_egress.Sink, checker license.Checker) {
	if sink == nil || checker == nil {
		return
	}
	info := checker.Info()
	result := licenseSnapshotResult(info)
	detail := map[string]any{
		"tier":  string(info.Tier),
		"valid": info.Valid,
	}
	if info.Subject != "" {
		detail["subject"] = info.Subject
	}
	if !info.ExpiresAt.IsZero() {
		detail["expires_at"] = info.ExpiresAt.UTC().Format(time.RFC3339)
		detail["expiring"] = info.Expiring
	}
	if info.Reason != "" {
		detail["reason"] = info.Reason
	}
	if len(info.Features) > 0 {
		feats := make([]string, len(info.Features))
		for i, f := range info.Features {
			feats[i] = string(f)
		}
		detail["features"] = feats
	}
	_ = sink.Emit(context.Background(), audit_egress.Record{ //nolint:errcheck // best-effort licence snapshot
		Category: "license",
		Actor:    "rousseau",
		Verb:     "load",
		Object:   string(info.Tier),
		Result:   result,
		Detail:   detail,
	})
}

// licenseSnapshotResult picks the SIEM-facing outcome string
// based on the licence Info. Split out so the branching is
// unit-testable without spinning up the full daemon assembly.
func licenseSnapshotResult(info license.Info) string {
	if !info.Valid {
		if info.Tier == license.TierCore && (info.Reason == "" || info.Reason == "no license configured") {
			return "core"
		}
		return "invalid"
	}
	if info.Expiring {
		return "expiring"
	}
	return "active"
}

// buildSSO composes the [sso.Directory] + [sso.BindingStore]
// pair the daemon wires into the transport router.
//
// The Directory is nil when SSO is not configured (KindNone) so
// the router skips the /login command entirely — a clean OSS
// experience. Otherwise the licence gate + sso.New handle the
// three failure modes documented in internal/auth/sso/sso.go
// (unconfigured / unlicensed / bad-config) and return a Nop
// Directory that the router then hides.
//
// The BindingStore is always non-nil — either the real SQLite
// backing store (when SSO could plausibly be used) or a
// NoBindings fail-safe. The router's Lookup path guards on
// store errors, but nil would just be a panic waiting to happen.
func buildSSO(ctx context.Context, cfg config.SSOConfig, checker license.Checker, store *sqlitestore.Store, logger *slog.Logger) (sso.Directory, sso.BindingStore, error) {
	if cfg.Kind == "" {
		return nil, sso.NoBindings{}, nil
	}
	// Real store: even when the Directory returns Nop (licence
	// missing), the store may hold pre-existing bindings a
	// previously-licensed run persisted. Keeping those readable
	// lets an operator downgrade + re-upgrade without users
	// having to /login again.
	bindings, err := sqlitestore.NewSSOBindings(ctx, store)
	if err != nil {
		return nil, sso.NoBindings{}, fmt.Errorf("cli: sso bindings: %w", err)
	}
	dir := sso.New(sso.Config{
		Kind: sso.Kind(cfg.Kind),
		OIDC: sso.OIDCConfig{
			Issuer:            cfg.OIDC.Issuer,
			Audience:          cfg.OIDC.Audience,
			JWKSRefresh:       cfg.OIDC.JWKSRefresh,
			ClockSkew:         cfg.OIDC.ClockSkew,
			TransportMappings: transportMappingsFromConfig(cfg.OIDC.TransportMappings),
		},
	}, checker, logger)
	// When SCIM is also configured, adapt its store into
	// [sso.DirectoryStore] so OIDCDirectory.ResolveTransportID
	// answers with real user data instead of ErrNotFound. The
	// underlying scim table is created here idempotently
	// (CREATE TABLE IF NOT EXISTS) — safe even when buildSCIM
	// also opened one against the same *sqlitestore.Store.
	if cfg.SCIM.Addr != "" && checker != nil && checker.IsEnabled(license.FeatureSSO) {
		scimStore, err := sqlitestore.NewSCIMStore(ctx, store)
		if err != nil {
			return nil, bindings, fmt.Errorf("cli: sso scim adapter: %w", err)
		}
		if oidcDir, ok := dir.(*sso.OIDCDirectory); ok {
			oidcDir.WithStore(newSCIMDirectoryStore(scimStore))
		}
	}
	return dir, bindings, nil
}

func transportMappingsFromConfig(in []config.SSOTransportMapping) []sso.TransportMapping {
	if len(in) == 0 {
		return nil
	}
	out := make([]sso.TransportMapping, len(in))
	for i, m := range in {
		out[i] = sso.TransportMapping{Transport: m.Transport, ClaimKey: m.ClaimKey}
	}
	return out
}

// TransportHandler returns the transport.Handler each transport
// should attach to its inbound loop. The chain is:
//
//	ratelimit.Wrap  →  resilience.Recover  →  Supervisor.Wrap  →  wiring.Router
//
// Supervisor sits closest to the router so two inbound messages for
// the same conversation cannot spawn two concurrent turns — the second
// folds into the first via Steer instead of racing a fresh `claude
// --resume` on the same session. Recover wraps that so a panic in the
// supervised path still cannot take the daemon down, and rate limiting
// is only applied when the ratelimit config has a non-nil entry for
// this transport.
//
// Each transport gets its own Supervisor+Registry so keys on different
// transports never collide.
func (w *daemonWiring) TransportHandler(name string, logger *slog.Logger) transport.Handler {
	sup := w.supervisorFor(name, logger)
	h := transport.Handler(w.routerFor(name))
	h = sup.Wrap(h)
	h = resilience.Recover(h, name, logger)
	if lim, ok := w.RateLimiters[name]; ok {
		h = ratelimit.Wrap(h, lim, name, "")
	}
	return h
}

// routerFor returns the transport.Router for transport name,
// building it on first call and caching it thereafter. The
// per-transport router carries the correct Transport name so
// Identity + SSO lookups key by (transport, sender) — a shared
// router with a hardcoded Transport would collide identical
// handles across transports.
//
// Reuses the same components the base w.Router was built from
// (agent, sessions, jidMap, allowlist, SSO surface, audit sink,
// approvals, build stamp) plus the Identities resolver so
// /whoami, /link, /unlink and the SSO identity stash all work.
// Kept behind supervisorMu since it's the same lifetime + safety
// story as supervisorFor.
func (w *daemonWiring) routerFor(name string) *transport.Router {
	w.supervisorMu.Lock()
	defer w.supervisorMu.Unlock()
	if r, ok := w.routers[name]; ok {
		return r
	}
	if w.routers == nil {
		w.routers = map[string]*transport.Router{}
	}
	// Reuse the identical option surface as w.Router (snapshotted
	// in routerOpts during assembleDaemon) — the only per-transport
	// difference is Transport and the Identity wiring.
	opts := w.routerOpts
	opts.Identity = w.Identities
	opts.Transport = name
	r := transport.NewRouter(w.Agent, w.Sessions, w.JIDMap, w.Logger, opts)
	w.routers[name] = r
	return r
}

// supervisorFor returns the Supervisor for transport name, creating
// (and remembering) it on first call. The lazy path is what lets a
// test that only exercises one transport avoid constructing a
// Registry for every transport in the codebase.
func (w *daemonWiring) supervisorFor(name string, logger *slog.Logger) *transport.Supervisor {
	w.supervisorMu.Lock()
	defer w.supervisorMu.Unlock()
	if sup, ok := w.supervisors[name]; ok {
		return sup
	}
	if w.supervisors == nil {
		w.supervisors = map[string]*transport.Supervisor{}
	}
	// Wire the shared progress bus into the Registry so each Turn's
	// Publish forwards events to it. The transport-side reporter
	// (whatsapp.startProgress → progress.Reporter → editingProgressSink)
	// subscribes on the same bus for its key, so the agent's per-tool
	// events reach the WhatsApp live-update message directly.
	sup := transport.NewSupervisor(
		control.NewRegistry(control.RegistryOptions{Bus: w.Progress}),
		logger,
	)
	w.supervisors[name] = sup
	return sup
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
