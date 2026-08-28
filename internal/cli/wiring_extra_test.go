package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// -- buildProvider ---------------------------------------------------

func TestBuildProvider_ClaudeCLIBarePrependsFlag(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider: "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{
			Bare:      true,
			ExtraArgs: []string{"--foo"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "claudecli", p.Name())
}

func TestBuildProvider_OpenAIFamily(t *testing.T) {
	for _, name := range []string{"openai", "openrouter", "ollama"} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{Provider: name}
			oa := config.OpenAIConfig{APIKey: "sk-test", Model: "m"}
			switch name {
			case "openai":
				cfg.OpenAI = oa
			case "openrouter":
				cfg.OpenRouter = oa
			case "ollama":
				cfg.Ollama = oa
			}
			p, err := buildProvider(cfg)
			require.NoError(t, err)
			assert.Equal(t, name, p.Name())
		})
	}
}

func TestBuildProvider_BedrockValidation(t *testing.T) {
	_, err := buildProvider(&config.Config{Provider: "bedrock"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bedrock.region")

	_, err = buildProvider(&config.Config{
		Provider: "bedrock",
		Bedrock:  config.BedrockConfig{Region: "us-west-2"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bedrock.model")
}

func TestBuildProvider_BedrockConstructs(t *testing.T) {
	isolateAWSEnv(t)
	p, err := buildProvider(&config.Config{
		Provider: "bedrock",
		Bedrock: config.BedrockConfig{
			Region: "us-west-2", Model: "anthropic.claude-sonnet-4-6", MaxTokens: 1024,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "bedrock", p.Name())
}

func TestBuildProvider_VertexValidation(t *testing.T) {
	_, err := buildProvider(&config.Config{Provider: "vertex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex.{project, region, model}")
}

func TestBuildProvider_VertexCredentialsFailureSurfaces(t *testing.T) {
	// A credentials file that does not parse keeps the failure local:
	// vertex.New reads it before it ever reaches Google's endpoints.
	bad := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(bad, []byte("not json"), 0o600))

	_, err := buildProvider(&config.Config{
		Provider: "vertex",
		Vertex: config.VertexConfig{
			Project: "p", Region: "us-central1", Model: "claude",
			CredentialsFile: bad,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex")
}

func TestBuildProvider_RouterDelegates(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider: "router",
		Router: config.RouterConfig{
			Default: "main",
			Providers: map[string]config.RouterChildConfig{
				"main": {Kind: "openai", APIKey: "sk", Model: "gpt-4"},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "router", p.Name())
}

func TestBuildChildProvider_BedrockConstructs(t *testing.T) {
	isolateAWSEnv(t)
	p, err := buildChildProvider("b", config.RouterChildConfig{
		Kind: "bedrock", Region: "us-west-2", Model: "anthropic.claude", Profile: "",
	})
	require.NoError(t, err)
	assert.Equal(t, "bedrock", p.Name())
}

func TestBuildChildProvider_VertexCredentialsFailureSurfaces(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o600))
	_, err := buildChildProvider("v", config.RouterChildConfig{
		Kind: "vertex", Project: "p", Region: "r", Model: "m", CredentialsFile: bad,
	})
	require.Error(t, err)
}

// isolateAWSEnv points the AWS SDK at empty config files and disables
// IMDS so the Bedrock constructor resolves purely from static env —
// no ~/.aws lookups, no metadata-service round trip.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-west-2")
}

// -- buildRateLimiters ----------------------------------------------

func TestBuildRateLimiters_EmptyConfigReturnsNil(t *testing.T) {
	got, err := buildRateLimiters(config.RateLimitConfig{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBuildRateLimiters_DefaultAppliesToEveryKnownTransport(t *testing.T) {
	got, err := buildRateLimiters(config.RateLimitConfig{Default: "10r/1m"})
	require.NoError(t, err)
	for _, name := range []string{
		"whatsapp", "signal", "telegram", "matrix",
		"slack", "discord", "sms", "imessage", "email",
	} {
		assert.Contains(t, got, name)
	}
}

func TestBuildRateLimiters_InvalidDefaultErrors(t *testing.T) {
	_, err := buildRateLimiters(config.RateLimitConfig{Default: "ten per minute"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ratelimit: default")
}

func TestBuildRateLimiters_InvalidPerTransportErrors(t *testing.T) {
	_, err := buildRateLimiters(config.RateLimitConfig{
		Default:      "10r/1m",
		PerTransport: map[string]string{"slack": "nonsense"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ratelimit: slack")
}

func TestBuildRateLimiters_PerTransportOverridesDefault(t *testing.T) {
	got, err := buildRateLimiters(config.RateLimitConfig{
		Default:      "10r/1m",
		PerTransport: map[string]string{"slack": "1r/1s"},
		MaxKeys:      5,
	})
	require.NoError(t, err)
	require.Contains(t, got, "slack")
	assert.NotNil(t, got["slack"])
}

func TestBuildRateLimiters_CustomTransportGetsItsOwnLimiter(t *testing.T) {
	got, err := buildRateLimiters(config.RateLimitConfig{
		PerTransport: map[string]string{"carrier-pigeon": "2r/1h"},
	})
	require.NoError(t, err)
	assert.Contains(t, got, "carrier-pigeon")
	assert.NotContains(t, got, "whatsapp", "no default means known transports stay unlimited")
}

func TestBuildRateLimiters_InvalidCustomTransportErrors(t *testing.T) {
	_, err := buildRateLimiters(config.RateLimitConfig{
		PerTransport: map[string]string{"carrier-pigeon": "??"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "carrier-pigeon")
}

// -- integrationsFromConfig -----------------------------------------

func TestIntegrationsFromConfig_NilConfigIsZero(t *testing.T) {
	assert.Equal(t, "", integrationsFromConfig(nil).GitHub.Token)
}

func TestIntegrationsFromConfig_MapsEverySuite(t *testing.T) {
	got := integrationsFromConfig(&config.Config{
		Integrations: config.IntegrationsConfig{
			GitHub:   config.GitHubToolsConfig{Enabled: true, Token: "ghp", BaseURL: "https://ghe"},
			Slack:    config.SlackToolsConfig{Enabled: true, BotToken: "xoxb"},
			Google:   config.GoogleToolsConfig{Enabled: true},
			Linear:   config.LinearToolsConfig{Enabled: true, APIKey: "lin"},
			Stripe:   config.StripeToolsConfig{Enabled: true, SecretKey: "sk"},
			Composio: config.ComposioToolsConfig{Enabled: true, APIKey: "co", UserID: "u", Apps: []string{"gmail"}},
		},
	})
	assert.Equal(t, "ghp", got.GitHub.Token)
	assert.Equal(t, "https://ghe", got.GitHub.BaseURL)
	assert.Equal(t, "xoxb", got.Slack.BotToken)
	assert.True(t, got.Google.Enabled)
	assert.Nil(t, got.Google.TokenFn, "the OAuth broker is wired by the caller, not by config")
	assert.Equal(t, "lin", got.Linear.APIKey)
	assert.Equal(t, "sk", got.Stripe.SecretKey)
	assert.Equal(t, []string{"gmail"}, got.Composio.Apps)
}

// -- buildHooks -----------------------------------------------------

func TestBuildHooks_NoHooksReturnsNil(t *testing.T) {
	assert.Nil(t, buildHooks(config.HooksConfig{}, silentLogger()))
}

func TestBuildHooks_TranslatesEveryEvent(t *testing.T) {
	one := []config.HookConfig{{
		Name: "audit", Command: "/bin/true",
		Args: []string{"-x"}, Env: map[string]string{"K": "V"},
		TimeoutSeconds: 3,
	}}
	got := buildHooks(config.HooksConfig{
		PreToolUse:  one,
		PostToolUse: one,
		PreTurn:     one,
		PostTurn:    one,
		OnError:     one,
	}, silentLogger())
	require.NotNil(t, got)

	set, ok := got.(*hooks.Set)
	require.True(t, ok, "buildHooks must produce the default hook Set")

	// The runner must actually be wired for every event, not merely
	// non-nil: /bin/true prints nothing, which the Set treats as Allow.
	for _, e := range []hooks.Event{
		hooks.EventPreToolUse, hooks.EventPostToolUse,
		hooks.EventPreTurn, hooks.EventPostTurn, hooks.EventOnError,
	} {
		verdict, err := set.Run(context.Background(), e, []byte(`{}`))
		require.NoError(t, err)
		assert.Equal(t, hooks.DecisionAllow, verdict.Decision)
	}
}

func TestBuildHooks_SingleEventStillBuildsRunner(t *testing.T) {
	got := buildHooks(config.HooksConfig{
		PreTurn: []config.HookConfig{{Name: "n", Command: "/bin/true"}},
	}, silentLogger())
	assert.NotNil(t, got)
}

// -- skills wiring --------------------------------------------------

func TestResolveSkillsDir_UnresolvableHomeReturnsEmpty(t *testing.T) {
	noHome(t)
	assert.Equal(t, "", resolveSkillsDir(&Options{Config: &config.Config{}}))
}

func TestBuildSkillsProvider_EmptyDirReturnsNoProvider(t *testing.T) {
	noHome(t)
	p, err := buildSkillsProvider(&Options{Config: &config.Config{}})
	require.NoError(t, err)
	assert.Nil(t, p, "no resolvable skills dir means no skills provider")
}

func TestBuildSkillsProvider_LoadFailureSurfaces(t *testing.T) {
	_, err := buildSkillsProvider(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: brokenSkillsDir(t)}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read dir")
}

func TestBuildRecallProvider_SkipsCurrentSession(t *testing.T) {
	s, err := sqlitestore.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup

	got := buildRecallProvider(s)
	fts, ok := got.(*agent.FTSRecall)
	require.True(t, ok)
	sess := agent.NewSession("current")
	assert.Equal(t, sess.ID, fts.SkipSessionID(sess),
		"recall must exclude the conversation it is being injected into")
}

// brokenSkillsDir returns a path that is a regular file rather than a
// directory — the shape you get when an operator points
// agent.skills_dir at a single Markdown file. skills.Load's ReadDir
// then fails with ENOTDIR, which is distinct from "missing" and must
// not be swallowed.
func brokenSkillsDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skills-is-a-file")
	require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))
	return path
}

func TestLoadSkillsFromResolutionChain_PrimaryLoadFailureSurfaces(t *testing.T) {
	withSystemSkillsDir(t, t.TempDir())
	_, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: brokenSkillsDir(t)}},
		Logger: silentLogger(),
	})
	assert.Error(t, err)
}

func TestLoadSkillsFromResolutionChain_SystemLoadFailureWithNilLogger(t *testing.T) {
	userDir := t.TempDir()
	writeSkill(t, userDir, "user-only", "from user dir")
	withSystemSkillsDir(t, brokenSkillsDir(t))

	// Logger is nil here on purpose: the degrade path must not assume
	// a logger has been wired yet.
	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-only"}, skillNames(got))
}

func TestSkillsListCmd_LoadFailureSurfaces(t *testing.T) {
	withSystemSkillsDir(t, t.TempDir())
	cmd := newSkillsListCmd(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: brokenSkillsDir(t)}},
		Logger: silentLogger(),
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

func TestSkillsShowCmd_LoadFailureSurfaces(t *testing.T) {
	withSystemSkillsDir(t, t.TempDir())
	cmd := newSkillsShowCmd(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: brokenSkillsDir(t)}},
		Logger: silentLogger(),
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, []string{"anything"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read dir",
		"an unloadable skills dir must not be reported as a missing skill")
}

// -- assembleDaemon error paths --------------------------------------

func TestAssembleDaemon_StoreOpenFailureSurfaces(t *testing.T) {
	noHome(t)
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve home")
}

func TestAssembleDaemon_IntegrationFailureRollsBack(t *testing.T) {
	opts := makeDaemonOpts(t)
	// Google enabled with no OAuth broker wired: documented as an
	// operator-visible startup error rather than a silent skip.
	opts.Config.Integrations.Google.Enabled = true
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "google")
}

func TestAssembleDaemon_RateLimitParseFailureRollsBack(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.RateLimit = config.RateLimitConfig{Default: "not-a-rate"}
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ratelimit")
}

func TestAssembleDaemon_ApproverFailureRollsBack(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Agent.Approver = config.ApproverConfig{Mode: "ask-a-human"}
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver")
}

func TestAssembleDaemon_SkillsFailureRollsBack(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Agent.SkillsDir = brokenSkillsDir(t)
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read dir")
}

func TestAssembleDaemon_CronStoreFailureRollsBack(t *testing.T) {
	opts := makeDaemonOpts(t)
	shadowCronJobsName(t, opts.Config.State.Path)
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cron schema")
}

func TestAssembleDaemon_ClaudeCLIProviderGetsSessionCache(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "claudecli"
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup
	assert.Equal(t, "claudecli", wiring.Provider.Name())
	assert.NotNil(t, wiring.ClaudeCache)
}

func TestCleanup_NilSessionsIsNoOp(t *testing.T) {
	w := &daemonWiring{Logger: silentLogger()}
	assert.NoError(t, w.Cleanup())
}

func TestStartCron_SchedulerStartFailureSurfaces(t *testing.T) {
	opts := makeDaemonOpts(t)
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	// A cancelled context makes the scheduler's initial job-list sync
	// fail, which is the only way Start reports an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shutdown, err := wiring.startCron(ctx, func(context.Context, string, string) error { return nil }, silentLogger())
	require.Error(t, err)
	assert.Nil(t, shutdown)
}

// shadowTableName creates an INDEX carrying the name of one of the
// store's tables. SQLite shares a single object namespace across
// tables and indexes, so the matching `CREATE TABLE IF NOT EXISTS`
// then fails outright instead of silently no-opping — a compact
// stand-in for the schema drift an operator hits after a botched
// manual migration, and the only way to reach the daemon's
// constructor-rollback branches.
func shadowTableName(t *testing.T, path, table string) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }() //nolint:errcheck // test cleanup
	_, err = db.Exec(`CREATE INDEX ` + table + ` ON sessions(id)`)
	require.NoError(t, err)
}

func shadowCronJobsName(t *testing.T, path string) {
	t.Helper()
	shadowTableName(t, path, "cron_jobs")
}

// driftedCostSchema pre-creates session_costs without its cost_usd
// column, plus decoy indexes carrying the names the real schema wants.
// NewSessionCostStore's `IF NOT EXISTS` DDL then no-ops and the
// aggregate queries fail at read time — the shape of a half-applied
// migration.
func driftedCostSchema(t *testing.T, path string) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }() //nolint:errcheck // test cleanup
	_, err = db.Exec(`
CREATE TABLE session_costs (
    session_id TEXT, at TEXT, provider TEXT, model TEXT,
    input_tokens INTEGER, output_tokens INTEGER,
    cache_read INTEGER, cache_creation INTEGER
);
CREATE INDEX idx_session_costs_session_id_at ON sessions(id);
CREATE INDEX idx_session_costs_at ON sessions(title);`)
	require.NoError(t, err)
}

// driftedCronSchema pre-creates cron_jobs without last_run_at so
// NewCronStore succeeds but List fails.
func driftedCronSchema(t *testing.T, path string) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }() //nolint:errcheck // test cleanup
	_, err = db.Exec(`
CREATE TABLE cron_jobs (
    id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, cron_expr TEXT NOT NULL,
    prompt TEXT NOT NULL, deliver_to TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
);`)
	require.NoError(t, err)
}

func TestSessionCostCmd_CostStoreConstructionFailureSurfaces(t *testing.T) {
	opts := makeOpts(t)
	shadowSessionCostsName(t, opts.Config.State.Path)
	cmd := newSessionCostCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

func shadowSessionCostsName(t *testing.T, path string) {
	t.Helper()
	shadowTableName(t, path, "session_costs")
}

// TestAssembleDaemon_SidecarStoreFailuresRollBack walks the three
// sidecar constructors the daemon opens on top of the session store.
// Each failure must release the session handle rather than leaking the
// SQLite connection on the way out.
func TestAssembleDaemon_SidecarStoreFailuresRollBack(t *testing.T) {
	for _, table := range []string{"jid_sessions", "claude_sessions", "session_costs"} {
		t.Run(table, func(t *testing.T) {
			opts := makeDaemonOpts(t)
			shadowTableName(t, opts.Config.State.Path, table)
			_, err := assembleDaemon(context.Background(), opts, nil)
			require.Error(t, err)

			// The rollback released the handle, so a fresh open of the
			// same file must succeed.
			store, oerr := sqlitestore.Open(context.Background(), opts.Config.State.Path)
			require.NoError(t, oerr)
			require.NoError(t, store.Close())
		})
	}
}

func TestSessionCostCmd_QueryFailuresSurface(t *testing.T) {
	t.Run("single session", func(t *testing.T) {
		opts := makeOpts(t)
		driftedCostSchema(t, opts.Config.State.Path)
		cmd := newSessionCostCmd(opts)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		assert.Error(t, cmd.RunE(cmd, []string{"whatever"}))
	})
	t.Run("top sessions", func(t *testing.T) {
		opts := makeOpts(t)
		driftedCostSchema(t, opts.Config.State.Path)
		cmd := newSessionCostCmd(opts)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		assert.Error(t, cmd.RunE(cmd, nil))
	})
}

// TestCronListCmd_ReadFailureSurfaces drives the List error branch: the
// table exists (so NewCronStore succeeds) but is missing a column the
// read query projects.
func TestCronListCmd_ReadFailureSurfaces(t *testing.T) {
	opts := makeOpts(t)
	driftedCronSchema(t, opts.Config.State.Path)
	cmd := newCronListCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

// TestCronCmds_CronStoreConstructionFailures asserts every cron
// subcommand reports a schema problem instead of pretending the job
// table is empty.
func TestCronCmds_CronStoreConstructionFailures(t *testing.T) {
	tests := map[string]func(*testing.T, *Options) error{
		"add": func(t *testing.T, o *Options) error {
			t.Helper()
			c := newCronAddCmd(o)
			require.NoError(t, c.Flags().Set("name", "n"))
			require.NoError(t, c.Flags().Set("schedule", "0 * * * *"))
			require.NoError(t, c.Flags().Set("prompt", "p"))
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, nil)
		},
		"list": func(t *testing.T, o *Options) error {
			t.Helper()
			c := newCronListCmd(o)
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, nil)
		},
		"remove": func(t *testing.T, o *Options) error {
			t.Helper()
			c := newCronRemoveCmd(o)
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, []string{"n"})
		},
		"toggle": func(t *testing.T, o *Options) error {
			t.Helper()
			c := newCronToggleCmd(o, false)
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, []string{"n"})
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			opts := makeOpts(t)
			shadowCronJobsName(t, opts.Config.State.Path)
			err := run(t, opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cron schema")
		})
	}
}

func TestMCPCmd_CronStoreFailureSurfaces(t *testing.T) {
	withStdio(t)
	opts := makeOpts(t)
	shadowCronJobsName(t, opts.Config.State.Path)
	cmd := newMCPCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cron schema")
}

func TestCronListCmd_RendersDisabledJobAndLastRun(t *testing.T) {
	opts := makeOpts(t)
	store, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	cs, err := sqlitestore.NewCronStore(context.Background(), store)
	require.NoError(t, err)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID: "job-id-0001", Name: "nightly", CronExpr: "0 3 * * *",
		Prompt: "summarise", DeliverTo: "1@s.whatsapp.net", Enabled: false,
	}))
	require.NoError(t, cs.RecordRun(context.Background(), "job-id-0001", time.Now().UTC()))
	require.NoError(t, store.Close())

	cmd := newCronListCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	out := buf.String()
	assert.Contains(t, out, "off")
	assert.NotContains(t, out, "last=never")
}

// -- root / logger ---------------------------------------------------

func TestNewRoot_ConfigLoadFailureAborts(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("provider: [unclosed\n"), 0o600))

	opts := &Options{}
	root := NewRoot(opts)
	root.SetArgs([]string{"--config", bad, "status"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	assert.Error(t, root.ExecuteContext(context.Background()))
}

func TestNewLogger_RedactionOptOut(t *testing.T) {
	t.Setenv(envLogNoRedact, "1")
	buf := &bytes.Buffer{}
	newLogger("info", "json", buf).Info("hi", "api_key", "sk-ant-secret-value")
	assert.Contains(t, buf.String(), "sk-ant-secret-value",
		"opting out of redaction must actually emit the raw value")
}

func TestNewLogger_PhoneRuleOptIn(t *testing.T) {
	t.Setenv(envLogNoRedact, "0")
	t.Setenv(envLogRedactPhones, "1")
	buf := &bytes.Buffer{}
	newLogger("info", "json", buf).Info("hi", "sender", "+441234567890")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	assert.NotEqual(t, "+441234567890", rec["sender"], "the phone rule must mask E.164 numbers")
}

// -- whatsapp helpers ------------------------------------------------

func TestBuildTranscriber_MisconfiguredAudioFallsBackToLegacy(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			// whisper-cpp without model_file: buildTranscriberString
			// errors, so the legacy whatsapp.voice path takes over.
			Media: config.MediaConfig{Audio: config.MediaAudioConfig{Backend: "whisper-cpp"}},
			WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{
				Enabled: true, Binary: "whisper", Model: "tiny",
			}},
		},
		Logger: silentLogger(),
	}
	assert.NotNil(t, buildTranscriber(opts))
}

func TestResolveWhatsAppDSN_UnresolvableHomeErrors(t *testing.T) {
	noHome(t)
	_, err := resolveWhatsAppDSN("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve home")
}

// -- init write failure ---------------------------------------------

func TestRunInit_WriteFailureSurfaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// config.yaml already exists as a *directory*, so --force gets past
	// the existence check and os.WriteFile then fails.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "rousseau", "config.yaml"), 0o755))
	err := runInit(&bytes.Buffer{}, stdin("", "", "", ""), &Options{}, true)
	assert.Error(t, err)
}

// -- doctor defaults -------------------------------------------------

func TestCheckProvider_EmptyProviderDefaultsToClaudeCLI(t *testing.T) {
	got := checkProvider(context.Background(), &config.Config{})
	require.NotEmpty(t, got)
	assert.Equal(t, "provider.selected", got[0].Name)
	assert.Equal(t, "claudecli", got[0].Detail)
}

// TestLoadSkillsFromResolutionChain_SystemLoadFailureIsLogged pins the
// observable half of the degrade path: with a logger wired, an
// unreadable system bundle leaves a debug breadcrumb instead of
// failing the command.
func TestLoadSkillsFromResolutionChain_SystemLoadFailureIsLogged(t *testing.T) {
	userDir := t.TempDir()
	writeSkill(t, userDir, "user-only", "from user dir")
	withSystemSkillsDir(t, brokenSkillsDir(t))

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	got, err := loadSkillsFromResolutionChain(&Options{
		Config: &config.Config{Agent: config.AgentConfig{SkillsDir: userDir}},
		Logger: logger,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-only"}, skillNames(got))
	assert.Contains(t, buf.String(), "skills.system_load_skipped")
}
