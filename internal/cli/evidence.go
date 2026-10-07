package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

// evidenceSchema versions the pack's JSON shape. Bump it on any
// breaking change so downstream GRC tooling can branch on it.
const evidenceSchema = "rousseau.evidence/v1"

// evidencePack is what `rousseau evidence` emits: one JSON document an
// operator files as procurement or audit evidence. It holds no secrets:
// the config appears as a hash plus the secret-free summary `config
// validate` prints, and the audit HMAC key only as a fingerprint.
type evidencePack struct {
	Schema      string            `json:"schema"`
	GeneratedAt time.Time         `json:"generated_at"`
	Build       evidenceBuild     `json:"build"`
	Config      evidenceConfig    `json:"config"`
	License     evidenceLicense   `json:"license"`
	Audit       evidenceAudit     `json:"audit"`
	Controls    evidenceControls  `json:"controls"`
	Retention   evidenceRetention `json:"retention"`
	Runtime     *evidenceRuntime  `json:"runtime,omitempty"`
	Doctor      doctorReport      `json:"doctor"`
	Mapping     []evidenceMapping `json:"mapping"`
}

type evidenceBuild struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	GoVersion string `json:"go_version"`
}

type evidenceConfig struct {
	SHA256  string        `json:"sha256,omitempty"`
	Error   string        `json:"error,omitempty"`
	Summary configSummary `json:"summary"`
}

type evidenceLicense struct {
	Tier      string    `json:"tier"`
	Subject   string    `json:"subject,omitempty"`
	Valid     bool      `json:"valid"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Features  []string  `json:"features"`
	Reason    string    `json:"reason,omitempty"`
}

type evidenceAudit struct {
	EgressKind         string `json:"egress_kind"`
	Chained            bool   `json:"chained"`
	HMACKeySource      string `json:"hmac_key_source,omitempty"`
	HMACKeyFingerprint string `json:"hmac_key_fingerprint,omitempty"`
	ChainSequence      uint64 `json:"chain_sequence"`
	ChainHead          string `json:"chain_head,omitempty"`
	Error              string `json:"error,omitempty"`
}

type evidenceControls struct {
	ApproverMode       string `json:"approver_mode"`
	ApproverDefault    string `json:"approver_default,omitempty"`
	ApproverAllowRules int    `json:"approver_allow_rules"`
	ApproverDenyRules  int    `json:"approver_deny_rules"`
	RBACRules          int    `json:"rbac_rules"`
	OPAPolicy          bool   `json:"opa_policy"`
	BashSandbox        string `json:"bash_sandbox"`
	AllowUnsandboxed   bool   `json:"bash_allow_unsandboxed"`
	BashEnvPassthrough int    `json:"bash_env_passthrough"`
	FSRoot             string `json:"tools_fs_root,omitempty"`
	FSDenyExtra        int    `json:"tools_fs_deny_extra"`
	ToolTimeout        string `json:"tool_timeout"`
	MaxToolOutputBytes int    `json:"max_tool_output_bytes"`
}

type evidenceRetention struct {
	SessionTTL         string `json:"session_ttl"`
	SessionIdleTimeout string `json:"session_idle_timeout"`
	ErasureCommand     string `json:"erasure_command"`
}

type evidenceRuntime struct {
	URL       string   `json:"url"`
	Ready     bool     `json:"ready"`
	ReadyCode int      `json:"ready_status"`
	Metrics   []string `json:"metrics,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// evidenceMapping ties a pack field to the regulation articles it
// supports and the doc that explains the operator's obligations.
type evidenceMapping struct {
	Framework string `json:"framework"`
	Reference string `json:"reference"`
	Fields    string `json:"fields"`
	Doc       string `json:"doc"`
}

// evidenceMappings is the article table. Keep it in step with
// docs/compliance/: each row points at the doc that explains what the
// operator still has to do beyond what the field shows.
var evidenceMappings = []evidenceMapping{
	{"EU AI Act", "Art. 12 record-keeping", "audit", "docs/compliance/eu-ai-act.md"},
	{"EU AI Act", "Art. 14 human oversight", "controls.approver_*", "docs/compliance/eu-ai-act.md"},
	{"EU AI Act", "Art. 15 accuracy, robustness, cybersecurity", "controls, doctor", "docs/compliance/eu-ai-act.md"},
	{"GDPR", "Art. 5(1)(e) storage limitation", "retention.session_ttl", "docs/compliance/gdpr.md"},
	{"GDPR", "Art. 17 right to erasure", "retention.erasure_command", "docs/compliance/gdpr.md"},
	{"GDPR", "Art. 32 security of processing", "controls, audit.chained", "docs/compliance/gdpr.md"},
	{"DORA", "Art. 9 protection and prevention", "controls", "docs/compliance/dora.md"},
	{"DORA", "Art. 10 detection", "audit, runtime", "docs/compliance/dora.md"},
	{"DORA", "Art. 28 ICT third-party risk", "config.summary.provider, config.summary.mcp_clients", "docs/compliance/dora.md"},
	{"SOC 2", "CC5.1 control activities", "controls.approver_*", "docs/compliance/soc2-readiness.md"},
	{"SOC 2", "CC7.2 system monitoring", "runtime, audit", "docs/compliance/soc2-readiness.md"},
	{"SOC 2", "CC8.1 change management", "build, config.sha256", "docs/compliance/soc2-readiness.md"},
	{"HIPAA", "§164.312(a) access control", "controls.approver_*", "docs/compliance/hipaa.md"},
	{"HIPAA", "§164.312(b) audit controls", "audit", "docs/compliance/hipaa.md"},
}

func newEvidenceCmd(opts *Options) *cobra.Command {
	var out, daemonURL string
	cmd := &cobra.Command{
		Use:   "evidence",
		Short: "Emit a compliance evidence pack (JSON) for procurement and audits",
		Long: "Collects the build stamp, config hash and summary, licence state, audit-chain\n" +
			"head, effective controls (approver, sandbox, filesystem, tool limits),\n" +
			"retention settings and the doctor report into one JSON document, with a\n" +
			"table mapping each field to the EU AI Act, GDPR, DORA, SOC 2 and HIPAA\n" +
			"articles it supports (see docs/compliance/). --daemon adds readiness and\n" +
			"a rousseau_* metrics snapshot from a running daemon's metrics listener.\n" +
			"The pack holds no secrets.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Config == nil {
				return fmt.Errorf("evidence: no config loaded")
			}
			pack := buildEvidence(cmd.Context(), opts, daemonURL, time.Now().UTC())
			return writeEvidence(cmd.OutOrStdout(), out, pack)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the pack to this file (mode 0600) instead of stdout")
	cmd.Flags().StringVar(&daemonURL, "daemon", "", "base URL of a running daemon's metrics listener, e.g. http://127.0.0.1:9100")
	return cmd
}

func writeEvidence(stdout io.Writer, path string, pack evidencePack) error {
	body, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return fmt.Errorf("evidence: marshal: %w", err)
	}
	body = append(body, '\n')
	if path == "" {
		_, err = stdout.Write(body)
		return err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("evidence: write %s: %w", path, err)
	}
	return nil
}

// buildEvidence assembles the pack. Every collector records its own
// failure inside the pack rather than aborting it: a partial pack that
// says what could not be read is better evidence than none.
func buildEvidence(ctx context.Context, opts *Options, daemonURL string, now time.Time) evidencePack {
	cfg := opts.Config
	chk := license.Load(license.Source{}, nil)
	pack := evidencePack{
		Schema:      evidenceSchema,
		GeneratedAt: now,
		Build:       evidenceBuild{Version: version, Commit: commit, BuiltAt: buildDate, GoVersion: runtime.Version()},
		Config:      evidenceConfigOf(opts),
		License:     evidenceLicenseOf(chk.Info()),
		Audit:       evidenceAuditOf(ctx, cfg),
		Controls:    evidenceControlsOf(cfg),
		Retention:   evidenceRetentionOf(cfg),
		Doctor:      summariseChecks(runChecks(ctx, cfg, chk)),
		Mapping:     evidenceMappings,
	}
	if daemonURL != "" {
		rt := evidenceRuntimeOf(ctx, daemonURL)
		pack.Runtime = &rt
	}
	return pack
}

// summariseChecks folds diag rows into the doctor --json shape.
func summariseChecks(rs []diagResult) doctorReport {
	var buf strings.Builder
	_ = renderJSON(&buf, rs) //nolint:errcheck // writing to a strings.Builder cannot fail
	var rep doctorReport
	_ = json.Unmarshal([]byte(buf.String()), &rep) //nolint:errcheck // round-trip of our own encoding
	return rep
}

func evidenceConfigOf(opts *Options) evidenceConfig {
	ec := evidenceConfig{Summary: summarizeConfig(opts)}
	raw, err := os.ReadFile(ec.Summary.Path) //nolint:gosec // the operator's own config path
	if err != nil {
		ec.Error = "config file not hashed: " + err.Error()
		return ec
	}
	sum := sha256.Sum256(raw)
	ec.SHA256 = hex.EncodeToString(sum[:])
	return ec
}

func evidenceLicenseOf(info license.Info) evidenceLicense {
	el := evidenceLicense{
		Tier:      string(info.Tier),
		Subject:   info.Subject,
		Valid:     info.Valid,
		ExpiresAt: info.ExpiresAt,
		Reason:    info.Reason,
		Features:  []string{},
	}
	for _, f := range info.Features {
		el.Features = append(el.Features, string(f))
	}
	return el
}

func evidenceAuditOf(ctx context.Context, cfg *config.Config) evidenceAudit {
	ae := cfg.Observability.AuditEgress
	ea := evidenceAudit{EgressKind: orNone(ae.Kind), Chained: ae.Chained}
	if ae.Chained {
		ea.HMACKeySource, ea.HMACKeyFingerprint = chainKeyEvidence(ae.ChainHMACKeyFile)
	}
	seq, head, err := loadChainHead(ctx, cfg.State)
	if err != nil {
		ea.Error = "audit chain head not read: " + err.Error()
		return ea
	}
	ea.ChainSequence, ea.ChainHead = seq, head
	return ea
}

// chainKeyEvidence names where the chain HMAC key comes from and a
// short fingerprint of it, so an auditor can tell two packs used the
// same key without the key leaving the host.
func chainKeyEvidence(file string) (source, fingerprint string) {
	source = file
	if source == "" {
		p, err := defaultChainKeyPath()
		if err != nil {
			return "(unresolved)", ""
		}
		source = p
	}
	key, err := os.ReadFile(source) //nolint:gosec // operator-configured key path
	if err != nil {
		return source + " (not readable)", ""
	}
	sum := sha256.Sum256(key)
	return source, hex.EncodeToString(sum[:8])
}

func loadChainHead(ctx context.Context, sc config.StateConfig) (uint64, string, error) {
	store, err := openSearchableStore(ctx, sc)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = store.Close() }() //nolint:errcheck // read-only
	cs, err := openAuditChainState(ctx, store)
	if err != nil {
		return 0, "", err
	}
	return cs.Load(ctx)
}

func evidenceControlsOf(cfg *config.Config) evidenceControls {
	ap := cfg.Agent.Approver
	sandbox := cfg.Tools.Bash.Sandbox.Kind
	if sandbox == "" {
		sandbox = "none"
	}
	toolTimeout := cfg.Agent.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 10 * time.Minute
	}
	maxOut := cfg.Agent.MaxToolOutputBytes
	if maxOut <= 0 {
		maxOut = 64 << 10
	}
	return evidenceControls{
		ApproverMode:       orNone(ap.Mode),
		ApproverDefault:    ap.Default,
		ApproverAllowRules: len(ap.Allow),
		ApproverDenyRules:  len(ap.Deny),
		RBACRules:          len(ap.RBAC.Rules),
		OPAPolicy:          ap.OPA.PolicyFile != "",
		BashSandbox:        sandbox,
		AllowUnsandboxed:   cfg.Tools.Bash.Sandbox.AllowUnsandboxed,
		BashEnvPassthrough: len(cfg.Tools.Bash.EnvPassthrough),
		FSRoot:             cfg.Tools.FS.Root,
		FSDenyExtra:        len(cfg.Tools.FS.Deny),
		ToolTimeout:        toolTimeout.String(),
		MaxToolOutputBytes: maxOut,
	}
}

func evidenceRetentionOf(cfg *config.Config) evidenceRetention {
	ttl := "kept until deleted"
	if cfg.State.SessionTTL > 0 {
		ttl = cfg.State.SessionTTL.String()
	}
	idle := "never"
	if cfg.Agent.SessionIdleTimeout > 0 {
		idle = cfg.Agent.SessionIdleTimeout.String()
	}
	return evidenceRetention{
		SessionTTL:         ttl,
		SessionIdleTimeout: idle,
		ErasureCommand:     "rousseau session delete-by-sender <sender> --yes",
	}
}

// evidenceRuntimeOf asks a running daemon for readiness and its
// rousseau_* metric samples.
func evidenceRuntimeOf(ctx context.Context, base string) evidenceRuntime {
	base = strings.TrimRight(base, "/")
	rt := evidenceRuntime{URL: base}
	client := &http.Client{Timeout: 5 * time.Second}
	code, _, err := httpGet(ctx, client, base+"/readyz")
	if err != nil {
		rt.Error = err.Error()
		return rt
	}
	rt.ReadyCode, rt.Ready = code, code == http.StatusOK
	code, body, err := httpGet(ctx, client, base+"/metrics")
	if err != nil || code != http.StatusOK {
		rt.Error = fmt.Sprintf("metrics: status %d: %v", code, err)
		return rt
	}
	rt.Metrics = rousseauSamples(body)
	return rt
}

func httpGet(ctx context.Context, client *http.Client, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, string(body), err
}

// rousseauSamples keeps the rousseau_* sample lines of a Prometheus
// exposition, dropping comments and the Go runtime/process families.
func rousseauSamples(exposition string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(exposition))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "rousseau_") {
			out = append(out, line)
		}
	}
	return out
}
