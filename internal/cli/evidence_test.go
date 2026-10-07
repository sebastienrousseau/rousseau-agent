package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// evidenceOpts is a config with a secret in it, a chain key on disk
// and a config file to hash.
func evidenceOpts(t *testing.T) (*Options, []byte) {
	t.Helper()
	dir := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef-chain-key")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "chain.key"), key, 0o600))
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("provider: anthropic\n"), 0o600))
	opts := makeDaemonOpts(t)
	opts.ConfigPath = cfgPath
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-secret-do-not-leak", Model: "m"}
	opts.Config.State.SessionTTL = 720 * time.Hour
	opts.Config.Agent.Approver = config.ApproverConfig{Mode: "pattern", Default: "deny", Allow: []config.PatternEntry{{Tool: "read", Match: ".*"}}}
	opts.Config.Observability.AuditEgress = config.AuditEgressConfig{Kind: "stdout", Chained: true, ChainHMACKeyFile: filepath.Join(dir, "chain.key")}
	return opts, key
}

func TestBuildEvidence_CollectsEverySection(t *testing.T) {
	opts, key := evidenceOpts(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	pack := buildEvidence(context.Background(), opts, "", now)

	assert.Equal(t, evidenceSchema, pack.Schema)
	assert.Equal(t, now, pack.GeneratedAt)
	assert.Len(t, pack.Config.SHA256, 64, "the config file is hashed")
	assert.Empty(t, pack.Config.Error)
	assert.Equal(t, "anthropic", pack.Config.Summary.Provider)

	assert.Equal(t, "core", pack.License.Tier)
	assert.NotNil(t, pack.License.Features)

	assert.True(t, pack.Audit.Chained)
	assert.Equal(t, opts.Config.Observability.AuditEgress.ChainHMACKeyFile, pack.Audit.HMACKeySource)
	assert.Len(t, pack.Audit.HMACKeyFingerprint, 16)
	assert.Empty(t, pack.Audit.Error, "a fresh store has a readable, empty chain")
	assert.Zero(t, pack.Audit.ChainSequence)

	assert.Equal(t, "pattern", pack.Controls.ApproverMode)
	assert.Equal(t, 1, pack.Controls.ApproverAllowRules)
	assert.Equal(t, "none", pack.Controls.BashSandbox)
	assert.True(t, pack.Controls.AllowUnsandboxed)
	assert.Equal(t, "10m0s", pack.Controls.ToolTimeout, "defaults are reported, not zero")
	assert.Equal(t, 65536, pack.Controls.MaxToolOutputBytes)

	assert.Equal(t, "720h0m0s", pack.Retention.SessionTTL)
	assert.Equal(t, "never", pack.Retention.SessionIdleTimeout)
	assert.Contains(t, pack.Retention.ErasureCommand, "delete-by-sender")

	assert.NotEmpty(t, pack.Doctor.Status)
	assert.NotEmpty(t, pack.Doctor.Checks)
	assert.Len(t, pack.Mapping, len(evidenceMappings))
	assert.Nil(t, pack.Runtime, "no --daemon, no runtime section")

	body, err := json.Marshal(pack)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "sk-secret-do-not-leak", "the pack holds no secrets")
	assert.NotContains(t, string(body), string(key))
}

func TestBuildEvidence_RecordsCollectorFailures(t *testing.T) {
	opts, _ := evidenceOpts(t)
	opts.ConfigPath = filepath.Join(t.TempDir(), "missing.yaml")
	opts.Config.Observability.AuditEgress.ChainHMACKeyFile = filepath.Join(t.TempDir(), "missing.key")
	opts.Config.State = config.StateConfig{Driver: "nosuchdriver"}
	pack := buildEvidence(context.Background(), opts, "", time.Now())

	assert.Contains(t, pack.Config.Error, "config file not hashed")
	assert.Empty(t, pack.Config.SHA256)
	assert.Contains(t, pack.Audit.HMACKeySource, "(not readable)")
	assert.Empty(t, pack.Audit.HMACKeyFingerprint)
	assert.Contains(t, pack.Audit.Error, "audit chain head not read")
}

func TestEvidenceRuntime_ReadinessAndMetrics(t *testing.T) {
	ready := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			if !ready {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/metrics":
			_, _ = w.Write([]byte("# HELP rousseau_x x\n# TYPE rousseau_x counter\nrousseau_x_total 3\ngo_goroutines 12\nrousseau_y{a=\"b\"} 1\n")) //nolint:errcheck // test fixture
		}
	}))
	defer srv.Close()

	rt := evidenceRuntimeOf(context.Background(), srv.URL+"/")
	assert.True(t, rt.Ready)
	assert.Equal(t, http.StatusOK, rt.ReadyCode)
	assert.Equal(t, []string{"rousseau_x_total 3", `rousseau_y{a="b"} 1`}, rt.Metrics, "only rousseau_* samples, no comments")
	assert.Empty(t, rt.Error)

	ready = false
	rt = evidenceRuntimeOf(context.Background(), srv.URL)
	assert.False(t, rt.Ready)
	assert.Equal(t, http.StatusServiceUnavailable, rt.ReadyCode)
}

func TestEvidenceRuntime_Unreachable(t *testing.T) {
	rt := evidenceRuntimeOf(context.Background(), "http://127.0.0.1:1")
	assert.False(t, rt.Ready)
	assert.NotEmpty(t, rt.Error)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	rt = evidenceRuntimeOf(context.Background(), srv.URL)
	assert.True(t, rt.Ready)
	assert.Contains(t, rt.Error, "metrics: status 404")

	_, _, err := httpGet(context.Background(), http.DefaultClient, "://bad")
	assert.Error(t, err)
}

func TestEvidenceCmd_WritesFileWithPrivateMode(t *testing.T) {
	opts, _ := evidenceOpts(t)
	out := filepath.Join(t.TempDir(), "pack.json")
	cmd := newEvidenceCmd(opts)
	cmd.SetArgs([]string{"--out", out})
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	var pack evidencePack
	raw, err := os.ReadFile(out) //nolint:gosec // test temp file
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &pack))
	assert.Equal(t, evidenceSchema, pack.Schema)
}

func TestEvidenceCmd_StdoutAndErrors(t *testing.T) {
	opts, _ := evidenceOpts(t)
	var buf bytes.Buffer
	cmd := newEvidenceCmd(opts)
	cmd.SetOut(&buf)
	cmd.SetArgs(nil)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.HasPrefix(buf.String(), "{\n"), "indented JSON on stdout")

	cmd = newEvidenceCmd(&Options{})
	cmd.SetArgs(nil)
	cmd.SetContext(context.Background())
	assert.ErrorContains(t, cmd.Execute(), "no config loaded")

	assert.ErrorContains(t, writeEvidence(&buf, filepath.Join(t.TempDir(), "no", "such", "dir", "p.json"), evidencePack{}), "evidence: write")
}

func TestEvidenceLicenseOf_ListsFeatures(t *testing.T) {
	el := evidenceLicenseOf(license.Info{Tier: "enterprise", Subject: "cust-1", Valid: true, Features: []license.Feature{"sso", "audit"}})
	assert.Equal(t, []string{"sso", "audit"}, el.Features)
	assert.True(t, el.Valid)
	assert.Equal(t, "cust-1", el.Subject)
}

func TestEvidenceRetention_And_ControlsDefaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.SessionIdleTimeout = 12 * time.Hour
	r := evidenceRetentionOf(cfg)
	assert.Equal(t, "kept until deleted", r.SessionTTL)
	assert.Equal(t, "12h0m0s", r.SessionIdleTimeout)

	cfg.Agent.ToolTimeout = time.Minute
	cfg.Agent.MaxToolOutputBytes = 10
	cfg.Tools.Bash.Sandbox.Kind = "nsjail"
	c := evidenceControlsOf(cfg)
	assert.Equal(t, "1m0s", c.ToolTimeout)
	assert.Equal(t, 10, c.MaxToolOutputBytes)
	assert.Equal(t, "nsjail", c.BashSandbox)
	assert.Equal(t, "(none)", c.ApproverMode)
}

// Every mapping row points at a compliance doc that exists.
func TestEvidenceMappings_DocsExist(t *testing.T) {
	for _, m := range evidenceMappings {
		_, err := os.Stat(filepath.Join("..", "..", m.Doc))
		assert.NoError(t, err, "%s %s -> %s", m.Framework, m.Reference, m.Doc)
	}
}
