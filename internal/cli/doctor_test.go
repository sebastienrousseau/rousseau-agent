package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

func TestMask_ShortReturnsStars(t *testing.T) {
	assert.Equal(t, "***", mask("abc"))
}

func TestMask_LongReturnsPrefixSuffix(t *testing.T) {
	got := mask("sk-ant-abcdefghij1234")
	assert.True(t, strings.HasPrefix(got, "sk-a"))
	assert.True(t, strings.HasSuffix(got, "1234"))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "0 B", humanBytes(0))
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.00 KiB", humanBytes(1024))
	assert.Equal(t, "1.00 MiB", humanBytes(1024*1024))
	assert.Equal(t, "1.00 GiB", humanBytes(1024*1024*1024))
	assert.Equal(t, "2.50 KiB", humanBytes(2560))
}

func TestHasFailures(t *testing.T) {
	assert.False(t, hasFailures(nil))
	assert.False(t, hasFailures([]diagResult{{Status: "ok"}, {Status: "warn"}}))
	assert.True(t, hasFailures([]diagResult{{Status: "ok"}, {Status: "fail"}}))
}

func TestRenderReport_AllStatuses(t *testing.T) {
	buf := &bytes.Buffer{}
	renderReport(buf, []diagResult{
		{Name: "ok-thing", Status: "ok", Detail: "good"},
		{Name: "warn-thing", Status: "warn", Detail: "iffy"},
		{Name: "fail-thing", Status: "fail", Detail: "broken"},
		{Name: "info-thing", Status: "info", Detail: "note"},
		{Name: "unknown-thing", Status: "mystery", Detail: "?"},
	})
	out := buf.String()
	assert.Contains(t, out, "✔")
	assert.Contains(t, out, "!")
	assert.Contains(t, out, "✘")
	assert.Contains(t, out, "·")
	assert.Contains(t, out, "?")
}

func TestVersionOf_HappyPath(t *testing.T) {
	// GNU coreutils' true --version prints a banner; BusyBox's does not.
	// Either way the exit code is 0 and versionOf must not error.
	_, err := versionOf(context.Background(), "true")
	require.NoError(t, err)
}

func TestVersionOf_MissingBinary(t *testing.T) {
	_, err := versionOf(context.Background(), "/nonexistent/binary")
	assert.Error(t, err)
}

func TestCountSessions_NonExistentPath(t *testing.T) {
	_, err := countSessions("/nonexistent/path/db.sqlite")
	assert.Error(t, err)
}

func TestCheckBuild(t *testing.T) {
	got := checkBuild()
	require.Len(t, got, 2)
	assert.Equal(t, "build.version", got[0].Name)
	assert.Equal(t, "build.go", got[1].Name)
	assert.Equal(t, "info", got[0].Status)
}

func TestCheckProvider_ClaudeCLIMissingBinary(t *testing.T) {
	cfg := &config.Config{
		Provider:  "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{Binary: "/definitely/not/on/path"},
	}
	got := checkProvider(context.Background(), cfg)
	// First entry: provider.selected (info)
	assert.Equal(t, "provider.selected", got[0].Name)
	// Second: missing binary → fail
	require.Len(t, got, 2)
	assert.Equal(t, "fail", got[1].Status)
}

func TestCheckProvider_ClaudeCLIFound(t *testing.T) {
	cfg := &config.Config{
		Provider:  "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{Binary: "true", PermissionMode: "acceptEdits"},
	}
	got := checkProvider(context.Background(), cfg)
	// Look for the ok binary status.
	var found bool
	for _, r := range got {
		if r.Name == "provider.claudecli.binary" && r.Status == "ok" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCheckProvider_AnthropicMissingKey(t *testing.T) {
	cfg := &config.Config{Provider: "anthropic"}
	got := checkProvider(context.Background(), cfg)
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckProvider_AnthropicWithKey(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Anthropic: config.AnthropicConfig{APIKey: "sk-ant-1234abcd5678efgh"},
	}
	got := checkProvider(context.Background(), cfg)
	var haveOK bool
	for _, r := range got {
		if r.Name == "provider.anthropic.api_key" && r.Status == "ok" {
			haveOK = true
		}
	}
	assert.True(t, haveOK)
}

func TestCheckProvider_UnknownProvider(t *testing.T) {
	cfg := &config.Config{Provider: "gemini"}
	got := checkProvider(context.Background(), cfg)
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckState_NonExistentIsInfo(t *testing.T) {
	cfg := &config.Config{State: config.StateConfig{Path: filepath.Join(t.TempDir(), "sessions.db")}}
	got := checkState(cfg)
	// db_size should be info (does not exist yet).
	var haveInfo bool
	for _, r := range got {
		if r.Name == "state.db_size" && r.Status == "info" {
			haveInfo = true
		}
	}
	assert.True(t, haveInfo)
}

func TestCheckState_ExistingFileIsOK(t *testing.T) {
	// Point at any existing file; the size check only needs a stat.
	path := filepath.Join(t.TempDir(), "sessions.db")
	require.NoError(t, os.WriteFile(path, []byte("dummy"), 0o600))
	cfg := &config.Config{State: config.StateConfig{Path: path}}
	got := checkState(cfg)
	var haveSize bool
	for _, r := range got {
		if r.Name == "state.db_size" {
			haveSize = true
		}
	}
	assert.True(t, haveSize)
}

func TestCheckWhatsApp_VoiceEnabledMissingBinary(t *testing.T) {
	cfg := &config.Config{
		WhatsApp: config.WhatsAppConfig{
			Voice: config.VoiceConfig{Enabled: true, Binary: "/not/on/path/whisper"},
		},
	}
	got := checkWhatsApp(cfg)
	var haveFail bool
	for _, r := range got {
		if r.Name == "whatsapp.voice.binary" && r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckWhatsApp_VoiceDisabled(t *testing.T) {
	cfg := &config.Config{}
	got := checkWhatsApp(cfg)
	var haveInfo bool
	for _, r := range got {
		if r.Name == "whatsapp.voice" && r.Status == "info" {
			haveInfo = true
		}
	}
	assert.True(t, haveInfo)
}

func TestCheckConfig(t *testing.T) {
	cfg := &config.Config{
		Log:      config.LogConfig{Level: "info", Format: "text"},
		Agent:    config.AgentConfig{MaxIterations: 16},
		WhatsApp: config.WhatsAppConfig{ReplyHeader: "hi\n\n"},
	}
	got := checkConfig(cfg)
	assert.NotEmpty(t, got)
}

func TestRunChecks_ComposesAllSections(t *testing.T) {
	cfg := &config.Config{Provider: "claudecli", ClaudeCLI: config.ClaudeCLIConfig{Binary: "true"}}
	got := runChecks(context.Background(), cfg, license.Core())
	assert.NotEmpty(t, got)
	// The license row is always present regardless of tier — that's
	// the whole point of §2.3.
	var haveLicense bool
	for _, r := range got {
		if strings.HasPrefix(r.Name, "identity.license") {
			haveLicense = true
		}
	}
	assert.True(t, haveLicense, "runChecks must emit at least one identity.license row")
}

func TestCheckLicense_NilCheckerRendersAsCore(t *testing.T) {
	// Defensive: a nil checker (upstream wiring bug) must render
	// as core, not panic.
	got := checkLicense(nil)
	require.NotEmpty(t, got)
	assert.Equal(t, "identity.license.tier", got[0].Name)
	assert.Equal(t, "info", got[0].Status)
	assert.Equal(t, "core", got[0].Detail)
}

func TestCheckLicense_CoreIsBareInfoRow(t *testing.T) {
	// Vast-majority OSS install: one bare info row. No noise.
	got := checkLicense(license.Core())
	require.Len(t, got, 1, "core install must not surface subject/features/expiry rows")
	assert.Equal(t, "identity.license.tier", got[0].Name)
	assert.Equal(t, "info", got[0].Status)
	assert.Equal(t, "core", got[0].Detail)
}

func TestCheckLicense_ValidLicenseSurfacesAllRows(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-abcd-1234",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(180 * 24 * time.Hour).Unix(),
	})
	got := checkLicense(chk)

	var byName = make(map[string]diagResult, len(got))
	for _, r := range got {
		byName[r.Name] = r
	}

	require.Contains(t, byName, "identity.license.tier")
	assert.Equal(t, "ok", byName["identity.license.tier"].Status)
	assert.Equal(t, "enterprise", byName["identity.license.tier"].Detail)

	require.Contains(t, byName, "identity.license.subject")
	assert.Equal(t, "cust-abcd-1234", byName["identity.license.subject"].Detail)

	require.Contains(t, byName, "identity.license.features")
	assert.Contains(t, byName["identity.license.features"].Detail, "sso")
	assert.Contains(t, byName["identity.license.features"].Detail, "audit_egress")

	require.Contains(t, byName, "identity.license.expires_at")
	assert.Equal(t, "info", byName["identity.license.expires_at"].Status)
	assert.Contains(t, byName["identity.license.expires_at"].Detail, "in ")
}

func TestCheckLicense_ExpiringLicenseWarnsWithRenewHint(t *testing.T) {
	// Inside ExpiryWarnWindow: tier row + expires_at row both warn.
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-soon-exp",
		Tier:      license.TierTeam,
		ExpiresAt: time.Now().Add(3 * 24 * time.Hour).Unix(),
	})
	got := checkLicense(chk)

	var tierRow, expRow diagResult
	for _, r := range got {
		switch r.Name {
		case "identity.license.tier":
			tierRow = r
		case "identity.license.expires_at":
			expRow = r
		}
	}
	assert.Equal(t, "warn", tierRow.Status, "tier row must warn inside expiry window")
	assert.Equal(t, "warn", expRow.Status)
	assert.Contains(t, expRow.Detail, "renew soon")
}

func TestCheckLicense_BadSignatureReasonRendersFail(t *testing.T) {
	// A licence that fails verification never lifts the checker
	// above core — but Info.Reason carries the diagnostic. Doctor
	// must render that as a fail row so paying customers can't
	// miss the misconfiguration.
	t.Setenv(license.DefaultEnvVar, "not-a-real-token.signature")
	chk := license.Load(license.Source{}, nil)
	got := checkLicense(chk)
	require.NotEmpty(t, got)
	// The tier row should be a fail with a "core (reason)" detail.
	assert.Equal(t, "identity.license.tier", got[0].Name)
	assert.Equal(t, "fail", got[0].Status)
	assert.Contains(t, got[0].Detail, "core")
	// Detail contains parenthesised reason.
	assert.Contains(t, got[0].Detail, "(")
}

func TestCheckLicense_TokenValueNeverAppearsInOutput(t *testing.T) {
	// Sensitive-value hygiene: even in the failure path, the raw
	// token string must never surface in doctor output.
	sensitive := "SUPER-SECRET-TOKEN-DO-NOT-LEAK.abc"
	t.Setenv(license.DefaultEnvVar, sensitive)
	chk := license.Load(license.Source{}, nil)
	got := checkLicense(chk)
	buf := &bytes.Buffer{}
	renderReport(buf, got)
	assert.NotContains(t, buf.String(), sensitive, "raw licence token MUST NOT surface in doctor output")
}

func TestHumanDuration_Ranges(t *testing.T) {
	assert.Equal(t, "5s", humanDuration(5*time.Second))
	assert.Equal(t, "3m", humanDuration(3*time.Minute))
	assert.Equal(t, "2h", humanDuration(2*time.Hour))
	assert.Equal(t, "7d", humanDuration(7*24*time.Hour))
}

func TestRenderExpiry_ExpiredTokenShowsAgo(t *testing.T) {
	got := renderExpiry(license.Info{ExpiresAt: time.Now().Add(-48 * time.Hour)})
	assert.Contains(t, got, "expired")
	assert.Contains(t, got, "ago")
}

// signAndLoadLicense mints a valid Ed25519-signed license,
// installs a matching public key into the process's embedded
// keyring for the duration of the test, and exercises
// [license.Load] end-to-end. Guarantees the doctor tests
// verify the same code path production runs.
func signAndLoadLicense(t *testing.T, claims license.Claims) license.Checker {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// Swap the embedded keyring for the duration of the test.
	orig := license.RawKeys
	t.Cleanup(func() { license.RawKeys = orig })
	license.RawKeys = base64EncodeStd(pub)

	tok, err := license.SignPayload(claims, priv)
	require.NoError(t, err)
	t.Setenv(license.DefaultEnvVar, tok)
	return license.Load(license.Source{}, nil)
}

// base64EncodeStd is a tiny wrapper — kept inline so the test's
// dependencies stay legible. The license package parses keys as
// standard-alphabet base64, comma-separated.
func base64EncodeStd(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
