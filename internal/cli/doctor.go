package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

// diagResult is one row in the doctor report.
type diagResult struct {
	Name   string
	Status string // "ok", "warn", "fail", "info"
	Detail string
}

func newDoctorCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the local rousseau installation",
		Long: "Print a report of every runtime dependency, config choice, and\n" +
			"state location the daemon relies on. Use this before opening a\n" +
			"bug report or when the WhatsApp bridge does not respond.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			results := runChecks(cmd.Context(), opts.Config, license.Load(license.Source{}, nil))
			renderReport(w, results)
			if hasFailures(results) {
				return errors.New("one or more checks failed")
			}
			return nil
		},
	}
}

func runChecks(ctx context.Context, cfg *config.Config, chk license.Checker) []diagResult {
	var out []diagResult
	out = append(out, checkBuild()...)
	out = append(out, checkLicense(chk)...)
	out = append(out, checkSSO(ctx, cfg, chk)...)
	out = append(out, checkGovernance(cfg, chk)...)
	out = append(out, checkProvider(ctx, cfg)...)
	out = append(out, checkState(cfg)...)
	out = append(out, checkWhatsApp(cfg)...)
	out = append(out, checkConfig(cfg)...)
	return out
}

// checkGovernance renders identity.governance.* rows for the
// RBAC layer landed with ROADMAP §2.9. Only emitted when
// agent.approver.rbac.rules is non-empty — a bare install with
// no governance config produces zero rows.
//
// Warns loudly when rules are declared but the licence doesn't
// unlock the feature (the most common misconfiguration —
// "I wrote RBAC rules and they aren't taking effect").
func checkGovernance(cfg *config.Config, chk license.Checker) []diagResult {
	rbac := cfg.Agent.Approver.RBAC
	if len(rbac.Rules) == 0 {
		return nil
	}
	out := []diagResult{{
		Name:   "identity.governance.rbac.rules",
		Status: "info",
		Detail: fmt.Sprintf("%d rule(s) configured", len(rbac.Rules)),
	}}
	if chk == nil || !chk.IsEnabled(license.FeatureGovernanceAdvanced) {
		out = append(out, diagResult{
			Name:   "identity.governance.rbac.licensed",
			Status: "warn",
			Detail: "rules configured but licence does not unlock governance_advanced — rules are inert (see docs/COMMERCIAL.md)",
		})
	} else {
		out = append(out, diagResult{
			Name: "identity.governance.rbac.licensed", Status: "ok", Detail: "active",
		})
	}
	return out
}

// checkSSO renders identity.sso.* rows. Only emitted when the
// operator has configured SSO — a clean-install (no config) shows
// no SSO rows at all, keeping the OSS doctor output uncluttered.
//
// Rows:
//   - identity.sso.kind      — "oidc" (info) or "misconfigured" (fail)
//   - identity.sso.issuer    — configured issuer URL
//   - identity.sso.licensed  — ok/warn depending on the licence gate
//   - identity.sso.bindings  — count of live /login bindings
//
// The binding count is read straight from the SQLite store — the
// same pattern countSessions uses. It only surfaces when
// state.driver=sqlite; the postgres driver's follow-up will add
// the same query behind the same row.
func checkSSO(ctx context.Context, cfg *config.Config, chk license.Checker) []diagResult {
	if cfg.Auth.SSO.Kind == "" {
		return nil // OSS install — no rows, no noise
	}
	out := []diagResult{
		{Name: "identity.sso.kind", Status: "info", Detail: cfg.Auth.SSO.Kind},
	}
	if cfg.Auth.SSO.OIDC.Issuer != "" {
		out = append(out, diagResult{
			Name: "identity.sso.issuer", Status: "info", Detail: cfg.Auth.SSO.OIDC.Issuer,
		})
	} else if cfg.Auth.SSO.Kind == "oidc" {
		out = append(out, diagResult{
			Name:   "identity.sso.issuer",
			Status: "fail",
			Detail: "auth.sso.kind=oidc but auth.sso.oidc.issuer is empty",
		})
	}
	if chk == nil || !chk.IsEnabled(license.FeatureSSO) {
		out = append(out, diagResult{
			Name:   "identity.sso.licensed",
			Status: "warn",
			Detail: "SSO configured but licence does not unlock it — /login is inert (see docs/COMMERCIAL.md)",
		})
	} else {
		out = append(out, diagResult{
			Name: "identity.sso.licensed", Status: "ok", Detail: "active",
		})
	}
	if n, err := countSSOBindings(ctx, cfg.State); err == nil {
		out = append(out, diagResult{
			Name: "identity.sso.bindings", Status: "ok", Detail: fmt.Sprintf("%d active", n),
		})
	}
	return out
}

// checkLicense renders the identity.license.* rows. Reads solely
// from [license.Checker.Info] — never touches the raw token. On a
// core-tier install the rows are informational; on a paid tier
// they surface tier, subject, effective features, and expiry
// (with an amber warn inside [license.ExpiryWarnWindow]).
//
// A cryptographic / structural failure (bad signature, expired,
// malformed) leaves the checker on core but with a non-empty
// Info.Reason — that path renders as a fail row so paying
// customers see the misconfiguration at a glance.
func checkLicense(chk license.Checker) []diagResult {
	if chk == nil {
		// Defensive: any caller that forgets to plumb a checker
		// gets the same behaviour as an unlicensed install.
		chk = license.Core()
	}
	info := chk.Info()
	tier := string(info.Tier)

	tierStatus, tierDetail := licenseTierRow(info, tier)
	out := []diagResult{{Name: "identity.license.tier", Status: tierStatus, Detail: tierDetail}}

	if info.Subject != "" {
		out = append(out, diagResult{
			Name:   "identity.license.subject",
			Status: "info",
			Detail: info.Subject,
		})
	}
	if len(info.Features) > 0 {
		feats := make([]string, len(info.Features))
		for i, f := range info.Features {
			feats[i] = string(f)
		}
		out = append(out, diagResult{
			Name:   "identity.license.features",
			Status: "info",
			Detail: strings.Join(feats, ","),
		})
	}
	if !info.ExpiresAt.IsZero() {
		status := "info"
		if info.Expiring {
			status = "warn"
		}
		out = append(out, diagResult{
			Name:   "identity.license.expires_at",
			Status: status,
			Detail: renderExpiry(info),
		})
	}
	return out
}

// licenseTierRow picks the status icon + detail string for the
// primary tier row. Split out so the branching is obvious.
func licenseTierRow(info license.Info, tier string) (status, detail string) {
	switch {
	case info.Valid && info.Expiring:
		return "warn", tier
	case info.Valid:
		return "ok", tier
	case info.Reason == "" || info.Reason == "no license configured":
		// Vast-majority OSS path — a bare "core" row.
		return "info", tier
	default:
		// Cryptographic / structural failure — paying customer's
		// misconfiguration. Loud fail row so it can't be missed.
		return "fail", fmt.Sprintf("%s (%s)", tier, info.Reason)
	}
}

// renderExpiry formats the expiry timestamp + a human delta so
// operators can eyeball "how long do I have" without doing UTC
// math.
func renderExpiry(info license.Info) string {
	ts := info.ExpiresAt.UTC().Format(time.RFC3339)
	d := time.Until(info.ExpiresAt)
	if d < 0 {
		return fmt.Sprintf("%s (expired %s ago)", ts, humanDuration(-d))
	}
	out := fmt.Sprintf("%s (in %s)", ts, humanDuration(d))
	if info.Expiring {
		out += " — renew soon"
	}
	return out
}

// humanDuration renders a duration as a coarse human string. Not
// exact, deliberately — an operator doesn't care about minute
// precision on a 180-day countdown.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

func checkBuild() []diagResult {
	return []diagResult{
		{
			Name:   "build.version",
			Status: "info",
			Detail: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),
		},
		{
			Name:   "build.go",
			Status: "info",
			Detail: fmt.Sprintf("%s / %s / %s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
		},
	}
}

func checkProvider(ctx context.Context, cfg *config.Config) []diagResult {
	if cfg == nil {
		return nil
	}
	provider := cfg.Provider
	if provider == "" {
		provider = "claudecli"
	}
	out := []diagResult{{Name: "provider.selected", Status: "info", Detail: provider}}

	switch provider {
	case "claudecli":
		binary := cfg.ClaudeCLI.Binary
		if binary == "" {
			binary = "claude"
		}
		path, err := exec.LookPath(binary)
		if err != nil {
			out = append(out, diagResult{
				Name:   "provider.claudecli.binary",
				Status: "fail",
				Detail: fmt.Sprintf("%q not found on $PATH — install Claude Code or set claudecli.binary", binary),
			})
			return out
		}
		out = append(out, diagResult{Name: "provider.claudecli.binary", Status: "ok", Detail: path})

		vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if v, err := versionOf(vctx, path); err == nil {
			out = append(out, diagResult{Name: "provider.claudecli.version", Status: "ok", Detail: v})
		} else {
			out = append(out, diagResult{Name: "provider.claudecli.version", Status: "warn", Detail: err.Error()})
		}

		if cfg.ClaudeCLI.PermissionMode == "" {
			out = append(out, diagResult{
				Name:   "provider.claudecli.permission_mode",
				Status: "warn",
				Detail: "empty — `rousseau whatsapp` will default this to bypassPermissions",
			})
		} else {
			out = append(out, diagResult{Name: "provider.claudecli.permission_mode", Status: "ok", Detail: cfg.ClaudeCLI.PermissionMode})
		}

	case "anthropic":
		if cfg.Anthropic.APIKey == "" {
			out = append(out, diagResult{
				Name:   "provider.anthropic.api_key",
				Status: "fail",
				Detail: "provider=anthropic but no API key set (env ANTHROPIC_API_KEY or anthropic.api_key in config)",
			})
		} else {
			out = append(out, diagResult{
				Name:   "provider.anthropic.api_key",
				Status: "ok",
				Detail: "present (masked: " + mask(cfg.Anthropic.APIKey) + ")",
			})
		}
	default:
		out = append(out, diagResult{
			Name:   "provider.selected",
			Status: "fail",
			Detail: fmt.Sprintf("unknown provider %q", provider),
		})
	}
	return out
}

func checkState(cfg *config.Config) []diagResult {
	driver := cfg.State.Driver
	if driver == "" {
		driver = "sqlite"
	}
	out := []diagResult{{Name: "state.driver", Status: "info", Detail: driver}}

	switch driver {
	case "sqlite":
		path := cfg.State.Path
		if path == "" {
			home, _ := os.UserHomeDir() //nolint:errcheck // fall back to empty home; join still produces a valid probe path
			path = filepath.Join(home, ".local", "share", "rousseau", "sessions.db")
		}
		out = append(out, diagResult{Name: "state.path", Status: "info", Detail: path})
		if info, err := os.Stat(path); err == nil {
			out = append(out, diagResult{Name: "state.db_size", Status: "ok", Detail: humanBytes(info.Size())})
			if n, err := countSessions(path); err == nil {
				out = append(out, diagResult{Name: "state.sessions", Status: "ok", Detail: fmt.Sprintf("%d recorded", n)})
			}
		} else if os.IsNotExist(err) {
			out = append(out, diagResult{Name: "state.db_size", Status: "info", Detail: "does not exist yet (created on first run)"})
		} else {
			out = append(out, diagResult{Name: "state.db_size", Status: "fail", Detail: err.Error()})
		}
	case "postgres":
		if cfg.State.DSN == "" {
			out = append(out, diagResult{
				Name:   "state.dsn",
				Status: "fail",
				Detail: "state.driver=postgres but state.dsn is empty",
			})
		} else {
			out = append(out, diagResult{
				Name:   "state.dsn",
				Status: "ok",
				Detail: redactDSN(cfg.State.DSN),
			})
		}
	default:
		out = append(out, diagResult{
			Name:   "state.driver",
			Status: "fail",
			Detail: fmt.Sprintf("unknown driver %q (want sqlite or postgres)", driver),
		})
	}
	return out
}

// redactDSN masks the password segment of a libpq URL so the DSN
// can be surfaced in diagnostic output without leaking creds.
// Keeps the scheme + user + host so operators can confirm the
// right server is targeted.
func redactDSN(dsn string) string {
	// Cheap regexp-free redaction — pgx accepts both "url://" and
	// "keyword=value" forms. Only the URL form has a colon-
	// delimited password field to worry about; the keyword form
	// splits password onto its own token we leave to the operator
	// (documented in COMMERCIAL.md).
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	rest := dsn[i+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return dsn
	}
	userinfo := rest[:at]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return dsn
	}
	return dsn[:i+3] + userinfo[:colon] + ":***" + rest[at:]
}

func checkWhatsApp(cfg *config.Config) []diagResult {
	home, _ := os.UserHomeDir() //nolint:errcheck // fall back to empty home; diagnostic probe still meaningful
	waPath := filepath.Join(home, ".local", "share", "rousseau", "whatsapp.db")
	out := []diagResult{{Name: "whatsapp.store", Status: "info", Detail: waPath}}
	if info, err := os.Stat(waPath); err == nil {
		out = append(out, diagResult{Name: "whatsapp.paired", Status: "ok", Detail: fmt.Sprintf("db present, %s (device credentials cached)", humanBytes(info.Size()))})
	} else {
		out = append(out, diagResult{Name: "whatsapp.paired", Status: "warn", Detail: "no db yet — first launch of `rousseau whatsapp` will print a QR"})
	}

	if cfg.WhatsApp.Voice.Enabled {
		binary := cfg.WhatsApp.Voice.Binary
		if binary == "" {
			binary = "whisper"
		}
		if path, err := exec.LookPath(binary); err == nil {
			out = append(out, diagResult{Name: "whatsapp.voice.binary", Status: "ok", Detail: path})
		} else {
			out = append(out, diagResult{
				Name:   "whatsapp.voice.binary",
				Status: "fail",
				Detail: fmt.Sprintf("voice enabled but %q not on $PATH", binary),
			})
		}
	} else {
		out = append(out, diagResult{Name: "whatsapp.voice", Status: "info", Detail: "disabled"})
	}
	return out
}

func checkConfig(cfg *config.Config) []diagResult {
	out := []diagResult{
		{Name: "config.log_level", Status: "info", Detail: cfg.Log.Level},
		{Name: "config.log_format", Status: "info", Detail: cfg.Log.Format},
		{Name: "config.agent.max_iterations", Status: "info", Detail: fmt.Sprintf("%d", cfg.Agent.MaxIterations)},
	}
	if cfg.WhatsApp.ReplyHeader != "" {
		out = append(out, diagResult{Name: "config.whatsapp.reply_header", Status: "info", Detail: strings.ReplaceAll(cfg.WhatsApp.ReplyHeader, "\n", "\\n")})
	}
	return out
}

func versionOf(ctx context.Context, binary string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func mask(s string) string {
	if len(s) < 8 {
		return "***"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func humanBytes(n int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%.2f MiB", float64(n)/mib)
	case n >= kib:
		return fmt.Sprintf("%.2f KiB", float64(n)/kib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func countSessions(path string) (int, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // best-effort cleanup
	var n int
	err = db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// countSSOBindings peeks at the sqlite `sso_bindings` table without
// going through the constructor — matches countSessions's pattern.
// Only works when the state driver is sqlite; postgres returns nil
// (the doctor SSO row simply omits the binding count).
func countSSOBindings(_ context.Context, sc config.StateConfig) (int, error) {
	driver := sc.Driver
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" {
		return 0, fmt.Errorf("countSSOBindings: only sqlite is supported (got %q)", driver)
	}
	path := sc.Path
	if path == "" {
		home, _ := os.UserHomeDir() //nolint:errcheck // fall back to empty home; probe still valid
		path = filepath.Join(home, ".local", "share", "rousseau", "sessions.db")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // best-effort cleanup
	var n int
	// Use a parameterised now to keep the query cheap even without
	// the expires_at index (the schema creates the index though).
	err = db.QueryRow(
		"SELECT COUNT(*) FROM sso_bindings WHERE expires_at > ?",
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func hasFailures(rs []diagResult) bool {
	for _, r := range rs {
		if r.Status == "fail" {
			return true
		}
	}
	return false
}

func renderReport(w io.Writer, rs []diagResult) {
	maxName := 0
	for _, r := range rs {
		if l := len(r.Name); l > maxName {
			maxName = l
		}
	}
	for _, r := range rs {
		icon := "?"
		switch r.Status {
		case "ok":
			icon = "✔"
		case "warn":
			icon = "!"
		case "fail":
			icon = "✘"
		case "info":
			icon = "·"
		}
		fmt.Fprintf(w, "%s  %-*s  %s\n", icon, maxName, r.Name, r.Detail) //nolint:errcheck // CLI output; stdout write failures are unrecoverable
	}
}
