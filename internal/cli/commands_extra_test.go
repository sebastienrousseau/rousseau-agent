package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// noHome unsets $HOME so os.UserHomeDir reports an error. That is the
// only portable way to drive the "cannot resolve home" branches the
// CLI has on every default-path lookup.
func noHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "")
}

// -- status ---------------------------------------------------------

func TestStatusCmd_RendersReportForExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	sess := agent.NewSession("hello")
	sess.Append(agent.NewUserText("hi"))
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	opts := &Options{
		Config: &config.Config{State: config.StateConfig{Path: path}},
		Logger: silentLogger(),
	}
	cmd := newStatusCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	out := buf.String()
	assert.Contains(t, out, "state.path")
	assert.Contains(t, out, "sessions               1")
	assert.Contains(t, out, "last_activity_at", "a populated DB reports its last write")
}

func TestStatusCmd_EmptyPathFallsBackToHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newStatusCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil), "a pre-first-run install reports an empty snapshot")
	assert.Contains(t, buf.String(), "sessions               0")
}

func TestStatusCmd_UnresolvableHomeErrors(t *testing.T) {
	noHome(t)
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newStatusCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

func TestStatusCmd_CollectFailureSurfaces(t *testing.T) {
	// A regular file standing in for a directory makes os.Stat return
	// ENOTDIR — not ErrNotExist — so collectStatus propagates it.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	opts := &Options{
		Config: &config.Config{State: config.StateConfig{Path: filepath.Join(blocker, "sessions.db")}},
		Logger: silentLogger(),
	}
	cmd := newStatusCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

func TestCollectStatus_StatErrorOtherThanNotExist(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, err := collectStatus(context.Background(), filepath.Join(blocker, "nested.db"))
	assert.Error(t, err)
}

func TestRenderStatus_ShowsLastActivityWhenSet(t *testing.T) {
	buf := &bytes.Buffer{}
	renderStatus(buf, StatusReport{LastActivityAt: time.Now().Add(-90 * time.Second)})
	assert.Contains(t, buf.String(), "last_activity_at")
	assert.Contains(t, buf.String(), "ago")
}

// -- doctor ---------------------------------------------------------

func TestDoctorCmd_CleanRunReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Clear any inherited ROUSSEAU_LICENSE_KEY so the smoke test
	// doesn't accidentally consume a real developer licence from
	// the outer shell.
	t.Setenv("ROUSSEAU_LICENSE_KEY", "")
	opts := &Options{
		Config: &config.Config{
			Provider:  "claudecli",
			ClaudeCLI: config.ClaudeCLIConfig{Binary: "true", PermissionMode: "acceptEdits"},
		},
		Logger: silentLogger(),
	}
	cmd := newDoctorCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "build.version")
	assert.Contains(t, buf.String(), "identity.license.tier")
}

func TestDoctorCmd_FailingCheckReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ROUSSEAU_LICENSE_KEY", "")
	opts := &Options{
		Config: &config.Config{
			Provider:  "claudecli",
			ClaudeCLI: config.ClaudeCLIConfig{Binary: "/definitely/not/on/path"},
		},
		Logger: silentLogger(),
	}
	cmd := newDoctorCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one or more checks failed")
}

func TestCheckProvider_NilConfigReturnsNothing(t *testing.T) {
	assert.Nil(t, checkProvider(context.Background(), nil))
}

func TestCheckProvider_VersionProbeFailureIsWarn(t *testing.T) {
	// `false` exists on $PATH but exits non-zero for --version, so the
	// version probe degrades to a warning rather than a hard failure.
	got := checkProvider(context.Background(), &config.Config{
		Provider:  "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{Binary: "false"},
	})
	var sawVersionWarn, sawPermissionWarn bool
	for _, r := range got {
		if r.Name == "provider.claudecli.version" && r.Status == "warn" {
			sawVersionWarn = true
		}
		if r.Name == "provider.claudecli.permission_mode" && r.Status == "warn" {
			sawPermissionWarn = true
		}
	}
	assert.True(t, sawVersionWarn)
	assert.True(t, sawPermissionWarn, "empty permission_mode must warn about the bypass default")
}

func TestCheckState_EmptyPathUsesHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := checkState(&config.Config{})
	require.NotEmpty(t, got)
	// First row is now state.driver (default sqlite); the
	// state.path row that used to be first is now second.
	assert.Equal(t, "state.driver", got[0].Name)
	assert.Equal(t, "sqlite", got[0].Detail)
	require.GreaterOrEqual(t, len(got), 2)
	assert.Equal(t, "state.path", got[1].Name)
	assert.Contains(t, got[1].Detail, "rousseau")
}

func TestCheckState_PostgresDriverSurfacesRedactedDSN(t *testing.T) {
	got := checkState(&config.Config{
		State: config.StateConfig{
			Driver: "postgres",
			DSN:    "postgres://alice:supersecret@db.example:5432/rousseau?sslmode=require",
		},
	})
	require.NotEmpty(t, got)
	assert.Equal(t, "state.driver", got[0].Name)
	assert.Equal(t, "postgres", got[0].Detail)

	var dsnRow diagResult
	for _, r := range got {
		if r.Name == "state.dsn" {
			dsnRow = r
		}
	}
	assert.Equal(t, "ok", dsnRow.Status)
	assert.Contains(t, dsnRow.Detail, "alice:***@db.example", "password must be redacted")
	assert.NotContains(t, dsnRow.Detail, "supersecret", "raw password MUST NOT appear")
}

func TestCheckState_PostgresMissingDSNIsFail(t *testing.T) {
	got := checkState(&config.Config{State: config.StateConfig{Driver: "postgres"}})
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail, "driver=postgres without DSN must be a fail row")
}

func TestCheckState_UnknownDriverIsFail(t *testing.T) {
	got := checkState(&config.Config{State: config.StateConfig{Driver: "mysql"}})
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestRedactDSN_HandlesShapes(t *testing.T) {
	// URL with password → redacted.
	assert.Equal(t, "postgres://alice:***@host/db",
		redactDSN("postgres://alice:hunter2@host/db"))
	// URL without password → untouched.
	assert.Equal(t, "postgres://alice@host/db",
		redactDSN("postgres://alice@host/db"))
	// Keyword form (not covered by the redactor — pass-through).
	assert.Equal(t, "user=alice password=hunter2 host=db",
		redactDSN("user=alice password=hunter2 host=db"))
}

func TestCheckState_StatErrorIsFail(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	got := checkState(&config.Config{
		State: config.StateConfig{Path: filepath.Join(blocker, "sessions.db")},
	})
	var sawFail bool
	for _, r := range got {
		if r.Name == "state.db_size" && r.Status == "fail" {
			sawFail = true
		}
	}
	assert.True(t, sawFail)
}

func TestCheckState_RealDBReportsSessionCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, s.Save(context.Background(), agent.NewSession("x")))
	require.NoError(t, s.Close())

	got := checkState(&config.Config{State: config.StateConfig{Path: path}})
	var detail string
	for _, r := range got {
		if r.Name == "state.sessions" {
			detail = r.Detail
		}
	}
	assert.Equal(t, "1 recorded", detail)
}

func TestCountSessions_RealDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	n, err := countSessions(path)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestCheckWhatsApp_PairedStoreAndVoiceBinaryFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	waDir := filepath.Join(home, ".local", "share", "rousseau")
	require.NoError(t, os.MkdirAll(waDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(waDir, "whatsapp.db"), []byte("device"), 0o600))

	got := checkWhatsApp(&config.Config{
		WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{Enabled: true, Binary: "true"}},
	})
	var pairedOK, voiceOK bool
	for _, r := range got {
		if r.Name == "whatsapp.paired" && r.Status == "ok" {
			pairedOK = true
		}
		if r.Name == "whatsapp.voice.binary" && r.Status == "ok" {
			voiceOK = true
		}
	}
	assert.True(t, pairedOK)
	assert.True(t, voiceOK)
}

func TestCheckWhatsApp_DefaultsVoiceBinaryToWhisper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := checkWhatsApp(&config.Config{
		WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{Enabled: true}},
	})
	var detail string
	for _, r := range got {
		if r.Name == "whatsapp.voice.binary" {
			detail = r.Detail
		}
	}
	assert.NotEmpty(t, detail, "an enabled voice config must report on the whisper binary")
}

// -- init -----------------------------------------------------------

func TestInitCmd_RunEWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := newInitCmd(&Options{})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetIn(strings.NewReader("\n\n\n\n"))
	require.NoError(t, cmd.Flags().Set("force", "true"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	_, err := os.Stat(filepath.Join(home, ".config", "rousseau", "config.yaml"))
	require.NoError(t, err)
}

func TestPickProvider_OpenAIOpenRouterOllama(t *testing.T) {
	tests := []struct {
		choice string
		want   string
		lines  []string
		expect string
	}{
		{"3", "openai", []string{"sk-openai", "gpt-4o"}, "gpt-4o"},
		{"4", "openrouter", []string{"sk-or", "meta/llama"}, "meta/llama"},
		{"5", "ollama", []string{"qwen3"}, "qwen3"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			name, block := pickProvider(tc.choice, &bytes.Buffer{}, stdin(tc.lines...))
			assert.Equal(t, tc.want, name)
			assert.Contains(t, block, tc.expect)
		})
	}
}

func TestRunInit_WhatsAppNextStepsAndBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := &bytes.Buffer{}
	require.NoError(t, runInit(out, stdin("1", "", "1@s.whatsapp.net", ""), &Options{}, true))

	cfg, err := os.ReadFile(filepath.Join(home, ".config", "rousseau", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), "whatsapp:")
	assert.Contains(t, out.String(), "rousseau whatsapp --allow 1@s.whatsapp.net")
}

func TestRunInit_MkdirFailureSurfaces(t *testing.T) {
	// $HOME is a regular file, so MkdirAll on $HOME/.config/rousseau
	// cannot succeed.
	home := filepath.Join(t.TempDir(), "home-is-a-file")
	require.NoError(t, os.WriteFile(home, []byte("x"), 0o600))
	t.Setenv("HOME", home)
	err := runInit(&bytes.Buffer{}, stdin("", "", "", ""), &Options{}, true)
	assert.Error(t, err)
}

func TestRenderConfig_EmptyWorkspaceSkipsStateBlock(t *testing.T) {
	got := renderConfig("claudecli", "claudecli:\n  binary: \"claude\"\n", "", "", "")
	assert.NotContains(t, got, "state:")
	assert.Contains(t, got, "provider: claudecli")
}

func TestPrompt_FallbackOnEOF(t *testing.T) {
	got := prompt(&bytes.Buffer{}, bufio.NewReader(strings.NewReader("")), "q: ", "fallback")
	assert.Equal(t, "fallback", got)
}

// -- session cost ---------------------------------------------------

// seedCosts writes cost rows for two sessions so the cost command has
// something to aggregate.
func seedCosts(t *testing.T, path string) (string, string) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	costs, err := sqlitestore.NewSessionCostStore(context.Background(), store)
	require.NoError(t, err)

	rich, lean := "session-rich", "session-lean"
	require.NoError(t, costs.Record(context.Background(), sqlitestore.CostRecord{
		SessionID: rich, Provider: "anthropic", Model: "claude",
		Usage: agent.Usage{
			InputTokens: 1000, OutputTokens: 200,
			CacheReadInputTokens: 10, CacheCreationInputTokens: 5,
		},
		CostUSD: 0.5,
	}))
	require.NoError(t, costs.Record(context.Background(), sqlitestore.CostRecord{
		SessionID: lean, Provider: "anthropic", Model: "claude",
		Usage:   agent.Usage{InputTokens: 10, OutputTokens: 2},
		CostUSD: 0.01,
	}))
	return rich, lean
}

func TestSessionCostCmd_TopSessionsTable(t *testing.T) {
	opts := makeOpts(t)
	rich, _ := seedCosts(t, opts.Config.State.Path)

	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	out := buf.String()
	assert.Contains(t, out, "top 2 sessions by cost")
	assert.Contains(t, out, shortID(rich))
}

func TestSessionCostCmd_EmptyWindow(t *testing.T) {
	opts := makeOpts(t)
	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "(no cost data in window)")
}

func TestSessionCostCmd_SingleSessionTable(t *testing.T) {
	opts := makeOpts(t)
	rich, _ := seedCosts(t, opts.Config.State.Path)

	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{rich}))

	out := buf.String()
	assert.Contains(t, out, "session: "+rich)
	assert.Contains(t, out, "input:   1000")
	assert.Contains(t, out, "cost:    $0.5000")
}

func TestSessionCostCmd_SingleSessionJSON(t *testing.T) {
	opts := makeOpts(t)
	rich, _ := seedCosts(t, opts.Config.State.Path)

	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.Flags().Set("json", "true"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{rich}))

	out := buf.String()
	assert.Contains(t, out, `"session_id": "`+rich+`"`)
	assert.Contains(t, out, `"input_tokens": 1000`)
}

func TestSessionCostCmd_TopSessionsJSON(t *testing.T) {
	opts := makeOpts(t)
	seedCosts(t, opts.Config.State.Path)

	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.Flags().Set("json", "true"))
	require.NoError(t, cmd.Flags().Set("limit", "1"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), `"limit": 1`)
}

// TestSessionCostCmd_SummaryIgnoresSessionArg pins the --summary
// escape hatch: with a session id AND --summary, the top-N view wins.
func TestSessionCostCmd_SummaryIgnoresSessionArg(t *testing.T) {
	opts := makeOpts(t)
	rich, _ := seedCosts(t, opts.Config.State.Path)

	cmd := newSessionCostCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.Flags().Set("summary", "true"))
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{rich}))
	assert.Contains(t, buf.String(), "top 2 sessions by cost")
}

func TestSessionCostCmd_StoreOpenFailure(t *testing.T) {
	noHome(t)
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newSessionCostCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, nil))
}

// -- store-open failure paths shared by session and cron -------------

func TestSessionCmds_StoreOpenFailures(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	tests := map[string]func() error{
		"list": func() error {
			c := newSessionListCmd(opts)
			c.SetContext(context.Background())
			return c.RunE(c, nil)
		},
		"search": func() error {
			c := newSessionSearchCmd(opts)
			c.SetContext(context.Background())
			return c.RunE(c, []string{"q"})
		},
		"show": func() error {
			c := newSessionShowCmd(opts)
			c.SetContext(context.Background())
			return c.RunE(c, []string{"id"})
		},
		"delete": func() error {
			c := newSessionDeleteCmd(opts)
			require.NoError(t, c.Flags().Set("yes", "true"))
			c.SetContext(context.Background())
			return c.RunE(c, []string{"id"})
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			noHome(t)
			assert.Error(t, run())
		})
	}
}

func TestCronCmds_StoreOpenFailures(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	tests := map[string]func() error{
		"add": func() error {
			c := newCronAddCmd(opts)
			require.NoError(t, c.Flags().Set("name", "n"))
			require.NoError(t, c.Flags().Set("schedule", "0 * * * *"))
			require.NoError(t, c.Flags().Set("prompt", "p"))
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, nil)
		},
		"list": func() error {
			c := newCronListCmd(opts)
			c.SetOut(&bytes.Buffer{})
			c.SetContext(context.Background())
			return c.RunE(c, nil)
		},
		"remove": func() error {
			c := newCronRemoveCmd(opts)
			c.SetContext(context.Background())
			return c.RunE(c, []string{"n"})
		},
		"toggle": func() error {
			c := newCronToggleCmd(opts, true)
			c.SetContext(context.Background())
			return c.RunE(c, []string{"n"})
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			noHome(t)
			assert.Error(t, run())
		})
	}
}

func TestOpenCronStore_DispatchesOnDriver(t *testing.T) {
	// The old openCronStore(opts) helper opened SQLite directly at
	// a hardcoded path and errored if the parent dir was missing.
	// The new openCronStore(ctx, store) sits on top of an already-
	// open SearchableStore and dispatches on the underlying
	// concrete type — sqlite or postgres. Verify the sqlite branch
	// wires up an idempotent cron store on an in-memory sqlite base.
	tmp := t.TempDir()
	cfg := config.StateConfig{Path: tmp + "/s.db"}
	store, err := openSearchableStore(context.Background(), cfg)
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	cs, err := openCronStore(context.Background(), store)
	require.NoError(t, err)
	require.NotNil(t, cs)
	// List on an empty store returns an empty slice + nil — pins
	// that the constructor actually applied its schema (List would
	// error otherwise with "no such table: cron_jobs").
	jobs, err := cs.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestCronAddCmd_DuplicateNameSurfacesStoreError(t *testing.T) {
	opts := makeOpts(t)
	add := func() error {
		c := newCronAddCmd(opts)
		require.NoError(t, c.Flags().Set("name", "dupe"))
		require.NoError(t, c.Flags().Set("schedule", "0 * * * *"))
		require.NoError(t, c.Flags().Set("prompt", "p"))
		c.SetOut(&bytes.Buffer{})
		c.SetContext(context.Background())
		return c.RunE(c, nil)
	}
	require.NoError(t, add())
	assert.Error(t, add(), "cron_jobs.name is UNIQUE — the second add must fail")
}

func TestSessionSearchCmd_ReturnsHits(t *testing.T) {
	opts := makeOpts(t)
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	sess := agent.NewSession("kubernetes notes")
	sess.Append(agent.NewUserText("how do I drain a kubernetes node"))
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	cmd := newSessionSearchCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{"kubernetes"}))
	assert.Contains(t, buf.String(), "kubernetes notes")
}

func TestSessionSearchCmd_InvalidFTSQueryErrors(t *testing.T) {
	opts := makeOpts(t)
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	cmd := newSessionSearchCmd(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	assert.Error(t, cmd.RunE(cmd, []string{`"unterminated`}))
}

func TestSessionShowCmd_RendersToolUseAndResult(t *testing.T) {
	opts := makeOpts(t)
	s, err := sqlitestore.Open(context.Background(), opts.Config.State.Path)
	require.NoError(t, err)
	sess := agent.NewSession("with-tools")
	sess.Append(agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{
		{ToolUse: &agent.ToolUse{ID: "t1", Name: "read", Input: []byte(`{"path":"/x"}`)}},
	}})
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{
		{ToolResult: &agent.ToolResult{ToolUseID: "t1", Output: "file body"}},
	}})
	require.NoError(t, s.Save(context.Background(), sess))
	require.NoError(t, s.Close())

	cmd := newSessionShowCmd(opts)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, []string{sess.ID}))

	out := buf.String()
	assert.Contains(t, out, "→ read(")
	assert.Contains(t, out, "← file body")
}

// -- openStore / loadOrCreateSession --------------------------------

func TestOpenStore_UnresolvableHomeErrors(t *testing.T) {
	noHome(t)
	_, err := openStore(context.Background(), config.StateConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve home")
}

func TestOpenStore_MkdirFailureErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, err := openStore(context.Background(), config.StateConfig{Path: filepath.Join(blocker, "sessions.db")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create state dir")
}

func TestLoadOrCreateSession_SaveFailureSurfaces(t *testing.T) {
	store, err := openStore(context.Background(), config.StateConfig{Path: filepath.Join(t.TempDir(), "s.db")})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	_, err = loadOrCreateSession(context.Background(), store, "", "titled")
	assert.Error(t, err, "saving through a closed store must not be silently swallowed")
}
