// Package config resolves runtime configuration with precedence
// flag > env > file > default. Callers wire it via Load.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the resolved application configuration.
type Config struct {
	// Provider selects the LLM backend:
	//   "claudecli"  (default) — shells out to the local `claude` CLI
	//   "anthropic"            — direct Anthropic API (ANTHROPIC_API_KEY)
	//   "openai"               — OpenAI Chat Completions
	//   "openrouter"           — OpenRouter (openai config, BaseURL preset)
	//   "ollama"               — local ollama (openai config, BaseURL preset)
	Provider      string              `mapstructure:"provider"`
	Anthropic     AnthropicConfig     `mapstructure:"anthropic"`
	ClaudeCLI     ClaudeCLIConfig     `mapstructure:"claudecli"`
	OpenAI        OpenAIConfig        `mapstructure:"openai"`
	OpenRouter    OpenAIConfig        `mapstructure:"openrouter"`
	Ollama        OpenAIConfig        `mapstructure:"ollama"`
	Log           LogConfig           `mapstructure:"log"`
	State         StateConfig         `mapstructure:"state"`
	Agent         AgentConfig         `mapstructure:"agent"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	WhatsApp      WhatsAppConfig      `mapstructure:"whatsapp"`
	Signal        SignalConfig        `mapstructure:"signal"`
	Telegram      TelegramConfig      `mapstructure:"telegram"`
	Bedrock       BedrockConfig       `mapstructure:"bedrock"`
	Vertex        VertexConfig        `mapstructure:"vertex"`
	Matrix        MatrixConfig        `mapstructure:"matrix"`
	Slack         SlackConfig         `mapstructure:"slack"`
	Discord       DiscordConfig       `mapstructure:"discord"`
	SMS           SMSConfig           `mapstructure:"sms"`
	IMessage      IMessageConfig      `mapstructure:"imessage"`
	Email         EmailConfig         `mapstructure:"email"`
	RateLimit     RateLimitConfig     `mapstructure:"ratelimit"`
	Resilience    ResilienceConfig    `mapstructure:"resilience"`
	Recall        RecallConfig        `mapstructure:"recall"`
	Integrations  IntegrationsConfig  `mapstructure:"integrations"`
	MCP           MCPConfig           `mapstructure:"mcp"`
	Router        RouterConfig        `mapstructure:"router"`
	Hooks         HooksConfig         `mapstructure:"hooks"`
	Media         MediaConfig         `mapstructure:"media"`
	Tools         ToolsConfig         `mapstructure:"tools"`
	Auth          AuthConfig          `mapstructure:"auth"`
}

// AuthConfig groups authentication surfaces. Today only SSO has
// operator-facing knobs; other auth paths (static allowlist, local
// SQLite) live under their respective transport / state config.
type AuthConfig struct {
	SSO SSOConfig `mapstructure:"sso"`
}

// SSOConfig configures the enterprise SSO surface. Zero value
// leaves SSO disabled — the OSS static-allowlist path handles
// every request. Requires FeatureSSO on the licence to activate;
// see docs/COMMERCIAL.md §2.1.
type SSOConfig struct {
	// Kind selects the SSO backend. Empty (default) leaves SSO
	// disabled; "oidc" enables the OpenID Connect verifier.
	Kind string `mapstructure:"kind"`
	// OIDC configures the OIDC verifier. Ignored when kind != "oidc".
	OIDC SSOOIDCConfig `mapstructure:"oidc"`
	// BindingTTL bounds how long a /login binding stays valid on
	// the daemon side. Empty (default zero) uses the token's exp
	// claim as-is; a shorter TTL clips a long-lived token so a
	// mis-issued 1-year JWT doesn't unlock a chat identity for a
	// year. Recommended: 24h.
	BindingTTL time.Duration `mapstructure:"binding_ttl"`
	// SCIM configures the SCIM 2.0 Service Provider — the pull-
	// based counterpart to the /login bootstrap. Fills the
	// ResolveTransportID deferral from #114 by letting an IdP
	// push users + groups on their existing SCIM schedule. Zero
	// value leaves the SCIM server off. Requires FeatureSSO.
	SCIM SCIMConfig `mapstructure:"scim"`
}

// SCIMConfig configures the SCIM 2.0 Service Provider.
type SCIMConfig struct {
	// Addr binds the SCIM HTTP endpoint. Empty leaves the
	// server off. Standard IdPs expect https:// (behind a
	// reverse-proxy that terminates TLS); the daemon serves
	// plain HTTP.
	Addr string `mapstructure:"addr"`
	// BearerToken is the shared secret the IdP presents in
	// the Authorization: Bearer header. Required when Addr is
	// set. Rotate via secret-manager / env var.
	BearerToken string `mapstructure:"bearer_token"`
	// BaseURL is the daemon's externally-reachable URL used
	// for the SCIM Meta.Location field. Optional; empty uses
	// relative /scim/v2/... paths.
	BaseURL string `mapstructure:"base_url"`
}

// SSOOIDCConfig is the operator-facing view of internal/auth/sso's
// OIDCConfig. Kept as a separate type so the config schema and
// the wire type can evolve independently — mapstructure tags stay
// in one place.
type SSOOIDCConfig struct {
	// Issuer is the IdP's issuer URL (e.g. https://tenant.okta.com).
	// Required. The verifier fetches /.well-known/openid-configuration
	// under this base at first VerifyToken call.
	Issuer string `mapstructure:"issuer"`
	// Audience is the expected `aud` claim on inbound tokens.
	// Optional; when empty, aud is not checked. Recommended.
	Audience string `mapstructure:"audience"`
	// JWKSRefresh controls how often the verifier re-fetches the
	// IdP's JWKS. Zero uses the shipped 15-minute default.
	JWKSRefresh time.Duration `mapstructure:"jwks_refresh"`
	// ClockSkew tolerates NTP drift between the IdP and the
	// daemon. Zero uses the shipped 2-minute default.
	ClockSkew time.Duration `mapstructure:"clock_skew"`
	// TransportMappings maps custom / namespaced token claims to
	// per-transport IDs. Example: `{transport: slack, claim_key:
	// "https://schemas.example.com/slack_user_id"}` lifts an
	// Okta-issued Slack ID from the JWT into the resulting
	// Identity.TransportIDs["slack"] field.
	TransportMappings []SSOTransportMapping `mapstructure:"transport_mappings"`
}

// SSOTransportMapping is one entry in
// [SSOOIDCConfig.TransportMappings].
type SSOTransportMapping struct {
	Transport string `mapstructure:"transport"`
	ClaimKey  string `mapstructure:"claim_key"`
}

// ToolsConfig groups per-built-in-tool configuration. Today only
// `bash` has knobs (sandbox selection); more tools may follow.
type ToolsConfig struct {
	Bash BashConfig `mapstructure:"bash"`
}

// BashConfig configures the bash built-in tool. Zero value is the
// pre-sandbox behaviour: direct exec, no isolation.
type BashConfig struct {
	// TimeoutSeconds caps a single command. Zero uses 60s.
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// Sandbox selects the execution backend. Zero value uses "none"
	// (direct exec) to keep pre-existing configs working. Set
	// `kind: gvisor` / `kind: nsjail` / `kind: firecracker` to
	// isolate; policy fields below shape the argv the backend runs.
	Sandbox BashSandboxConfig `mapstructure:"sandbox"`
}

// BashSandboxConfig maps 1:1 to internal/tools/sandbox.Policy. Kept
// as its own config type so the config package does not import the
// sandbox package (loading order + test-isolation reasons).
type BashSandboxConfig struct {
	// Kind is the backend to use: "" or "none" (default, no
	// isolation), "gvisor", "nsjail", "firecracker".
	Kind string `mapstructure:"kind"`
	// NoNetwork enables the backend's network isolation. Ignored
	// when Kind is "none". Defaults ON for isolating backends —
	// see cli/bash_sandbox.go for the default resolution.
	NoNetwork *bool `mapstructure:"no_network"`
	// TmpdirRoot is the parent directory for per-invocation
	// tmpdirs. Empty falls back to os.TempDir().
	TmpdirRoot string `mapstructure:"tmpdir_root"`
	// WallclockSeconds caps subprocess elapsed time inside the
	// backend (defence in depth on top of the tool's own timeout).
	// Zero disables.
	WallclockSeconds int `mapstructure:"wallclock_seconds"`
	// CPUSeconds caps consumed CPU. Zero disables.
	CPUSeconds int `mapstructure:"cpu_seconds"`
	// MemoryMB caps address-space in MiB. Zero disables.
	MemoryMB int `mapstructure:"memory_mb"`
	// Readonly bindmounts (host paths, mounted at the same path RO
	// inside the sandbox).
	Readonly []string `mapstructure:"readonly"`
	// Writable bindmounts.
	Writable []string `mapstructure:"writable"`
}

// MediaConfig configures inbound media handling. Today only
// [MediaAudio] is populated; future work will add image / video.
type MediaConfig struct {
	Audio MediaAudioConfig `mapstructure:"audio"`
}

// MediaAudioConfig configures voice-note transcription. Absent /
// empty leaves transcription disabled — inbound audio messages are
// then ignored by every transport (matches the "no transcriber
// configured" branch each transport's Dispatch already has).
//
// Backend picks the implementation:
//   - `whisper-cpp` — shells out to a local whisper.cpp binary. Best
//     for compliance-constrained deployments; requires ModelFile to
//     be a readable ggml-*.bin.
//   - `openai-api` — calls OpenAI's /v1/audio/transcriptions.
//     Requires APIKey. Highest quality on accented/noisy audio.
type MediaAudioConfig struct {
	Backend        string `mapstructure:"backend"` // whisper-cpp | openai-api | ""
	ModelFile      string `mapstructure:"model_file"`
	Binary         string `mapstructure:"binary"`
	APIKey         string `mapstructure:"api_key"`
	BaseURL        string `mapstructure:"base_url"`
	Model          string `mapstructure:"model"`
	Language       string `mapstructure:"language"`
	TimeoutSeconds int    `mapstructure:"timeout_seconds"`
	MaxBytes       int    `mapstructure:"max_bytes"`
}

// HooksConfig configures lifecycle-hook scripts. Each event name maps
// to an ordered list of hook specs; hooks fire in declaration order
// and the first Deny verdict wins. See [internal/agent/hooks] for
// the payload/verdict JSON shapes.
//
// Example:
//
//	hooks:
//	  pre_tool_use:
//	    - name: no-secrets
//	      command: /etc/rousseau/hooks/no-secrets.sh
//	      timeout_seconds: 5
type HooksConfig struct {
	PreToolUse  []HookConfig `mapstructure:"pre_tool_use"`
	PostToolUse []HookConfig `mapstructure:"post_tool_use"`
	PreTurn     []HookConfig `mapstructure:"pre_turn"`
	PostTurn    []HookConfig `mapstructure:"post_turn"`
	OnError     []HookConfig `mapstructure:"on_error"`
}

// HookConfig is one hook attached to one event.
type HookConfig struct {
	Name           string            `mapstructure:"name"`
	Command        string            `mapstructure:"command"`
	Args           []string          `mapstructure:"args"`
	Env            map[string]string `mapstructure:"env"`
	TimeoutSeconds int               `mapstructure:"timeout_seconds"`
}

// RouterConfig configures the multi-model routing provider (Provider =
// "router"). Named children under Providers are themselves any of the
// supported provider kinds; Rules pick a child per request. See
// [internal/llm/router] for evaluation semantics.
//
// Example:
//
//	provider: router
//	router:
//	  default: sonnet
//	  rules:
//	    - if:  { message_len_max: 200, tool_use_count_max: 0 }
//	      use: haiku
//	    - if:  { tool_use_count_min: 3 }
//	      use: opus
//	  providers:
//	    haiku:  { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-haiku-4-5 }
//	    sonnet: { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-sonnet-4-6 }
//	    opus:   { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-opus-4-6 }
type RouterConfig struct {
	// Default names the provider key used when no rule matches.
	Default string `mapstructure:"default"`
	// Rules is the ordered list of routing decisions (first match wins).
	Rules []RouterRuleConfig `mapstructure:"rules"`
	// Providers maps a key (referenced by Rules.Use and Default) to
	// its concrete child-provider config.
	Providers map[string]RouterChildConfig `mapstructure:"providers"`
}

// RouterRuleConfig is one routing rule. Empty match fields disable the
// corresponding filter; all set filters are AND'd.
type RouterRuleConfig struct {
	Name            string `mapstructure:"name"`
	MessageLenMax   int    `mapstructure:"message_len_max"`
	MessageLenMin   int    `mapstructure:"message_len_min"`
	ToolUseCountMax int    `mapstructure:"tool_use_count_max"`
	ToolUseCountMin int    `mapstructure:"tool_use_count_min"`
	SessionIDPrefix string `mapstructure:"session_id_prefix"`
	Use             string `mapstructure:"use"`
}

// RouterChildConfig configures one child provider under
// router.providers. Fields are a union across the supported provider
// kinds — only the ones relevant to Kind are consulted.
type RouterChildConfig struct {
	Kind      string `mapstructure:"kind"` // anthropic | openai | openrouter | ollama | bedrock | vertex
	APIKey    string `mapstructure:"api_key"`
	BaseURL   string `mapstructure:"base_url"`
	Model     string `mapstructure:"model"`
	MaxTokens int64  `mapstructure:"max_tokens"`
	// bedrock-specific
	Region  string `mapstructure:"region"`
	Profile string `mapstructure:"profile"`
	// vertex-specific
	Project         string `mapstructure:"project"`
	CredentialsFile string `mapstructure:"credentials_file"`
}

// MCPConfig configures external MCP servers the agent should consume.
// Each entry under Clients spawns a subprocess at daemon start, walks
// its tools/list, and registers every discovered tool with the agent's
// [tools.Registry] under the name "mcp:<name>:<tool>".
//
// Example config.yaml:
//
//	mcp:
//	  clients:
//	    github:
//	      command: npx
//	      args: ['-y', '@modelcontextprotocol/server-github']
//	      env:
//	        GITHUB_PERSONAL_ACCESS_TOKEN: ${GITHUB_TOKEN}
//	    playwright:
//	      command: npx
//	      args: ['-y', '@modelcontextprotocol/server-playwright']
type MCPConfig struct {
	// Clients maps a server name (used as the tool-name prefix) to its
	// subprocess spec. Empty or absent map means no MCP clients are
	// started — the agent runs with local tools only.
	Clients map[string]MCPClientConfig `mapstructure:"clients"`
}

// MCPClientConfig describes one external MCP server subprocess.
type MCPClientConfig struct {
	// Command is the executable to spawn (resolved via $PATH).
	Command string `mapstructure:"command"`
	// Args are the command-line arguments passed to Command.
	Args []string `mapstructure:"args"`
	// Env are extra environment variables layered on top of the
	// daemon's own environment. Set an entry to "" to unset a
	// variable the parent process has.
	Env map[string]string `mapstructure:"env"`
	// StartTimeoutSeconds bounds the initialize-handshake window.
	// Zero uses the client default (30s).
	StartTimeoutSeconds int `mapstructure:"start_timeout_seconds"`
	// RequestTimeoutSeconds bounds each tools/list and tools/call
	// invocation. Zero uses the client default (60s).
	RequestTimeoutSeconds int `mapstructure:"request_timeout_seconds"`
}

// IntegrationsConfig groups the native tool-integration suites +
// the Composio adapter (§1 of docs/IMPLEMENTATION_PLAN_2026_07_16.md).
// Every field is opt-in via `enabled: true` under its section.
type IntegrationsConfig struct {
	GitHub   GitHubToolsConfig   `mapstructure:"github"`
	Slack    SlackToolsConfig    `mapstructure:"slack"`
	Google   GoogleToolsConfig   `mapstructure:"google"`
	Linear   LinearToolsConfig   `mapstructure:"linear"`
	Stripe   StripeToolsConfig   `mapstructure:"stripe"`
	Composio ComposioToolsConfig `mapstructure:"composio"`
}

// GitHubToolsConfig configures the native GitHub tool suite.
type GitHubToolsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Token   string `mapstructure:"token"`
	BaseURL string `mapstructure:"base_url"`
}

// SlackToolsConfig configures the native Slack tool suite (distinct
// from the SlackConfig transport — a bot token may be shared or
// separate).
type SlackToolsConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	BotToken string `mapstructure:"bot_token"`
}

// GoogleToolsConfig configures Gmail + Calendar + Drive.
type GoogleToolsConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// LinearToolsConfig configures the Linear GraphQL tools.
type LinearToolsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	APIKey  string `mapstructure:"api_key"`
}

// StripeToolsConfig configures the read-only Stripe tools.
type StripeToolsConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	SecretKey string `mapstructure:"secret_key"`
}

// ComposioToolsConfig configures the Composio adapter — registers
// every action visible to the authenticated user, or a subset
// filtered by Apps.
type ComposioToolsConfig struct {
	Enabled bool     `mapstructure:"enabled"`
	APIKey  string   `mapstructure:"api_key"`
	UserID  string   `mapstructure:"user_id"`
	Apps    []string `mapstructure:"apps"`
}

// RecallConfig configures the vector-based long-term memory (§9 of
// docs/IMPLEMENTATION_PLAN_2026_07_16.md). Zero-value disables recall
// entirely.
type RecallConfig struct {
	// Enabled toggles the entire subsystem.
	Enabled bool `mapstructure:"enabled"`
	// Embedder chooses the backend. Supported: "noop", "voyage",
	// "openai". "noop" produces zero-vectors — useful in tests + when
	// the operator is exercising storage without embedding cost.
	Embedder string `mapstructure:"embedder"`
	// EmbedderModel overrides the backend's default model.
	EmbedderModel string `mapstructure:"embedder_model"`
	// EmbedderAPIKey supplies credentials to the backend. Empty falls
	// back to the backend-specific env var (ROUSSEAU_VOYAGE_API_KEY
	// for voyage; ROUSSEAU_OPENAI_API_KEY then OPENAI_API_KEY for
	// openai).
	EmbedderAPIKey string `mapstructure:"embedder_api_key"`
	// EmbedderDims overrides auto-detected dimensionality; required
	// for non-well-known models.
	EmbedderDims int `mapstructure:"embedder_dims"`
	// ChunkTokens is the target chunk size for ingestion. Default 400.
	ChunkTokens int `mapstructure:"chunk_tokens"`
	// ChunkOverlap is the token overlap between adjacent chunks.
	// Default 40.
	ChunkOverlap int `mapstructure:"chunk_overlap"`
	// RetrievalK caps how many rows Recall returns per query.
	// Default 6.
	RetrievalK int `mapstructure:"retrieval_k"`
	// HybridWeight is the vector-vs-keyword blend passed to the
	// retriever. Default 0.7 (vector-heavy). Range [0, 1].
	HybridWeight float32 `mapstructure:"hybrid_weight"`
	// PurgeAfter drops rows older than this. Zero disables purge.
	// Format: any time.ParseDuration string (e.g. "4320h" for 180d).
	PurgeAfter string `mapstructure:"purge_after"`
}

// RateLimitConfig configures the per-JID token-bucket rate limiter.
// A zero-value config disables rate limiting entirely.
type RateLimitConfig struct {
	// Default is the fallback rate for transports without a specific
	// override. Format: "Nr/Duration", e.g. "10r/1m".
	Default string `mapstructure:"default"`
	// PerTransport overrides Default on a per-transport basis. Keys
	// are transport names ("whatsapp", "slack", "email", …).
	PerTransport map[string]string `mapstructure:"per_transport"`
	// MaxKeys caps the LRU of tracked senders. Zero uses the built-in
	// default (10 000).
	MaxKeys int `mapstructure:"max_keys"`
	// DeniedReply overrides the user-facing string returned when a
	// message is rate-limited. Empty uses ratelimit.DefaultDeniedReply.
	DeniedReply string `mapstructure:"denied_reply"`
}

// ResilienceConfig configures panic-recovery and circuit-breaker
// middleware.
type ResilienceConfig struct {
	// CircuitBreaker configures every provider-side breaker with the
	// same settings. Zero-value uses gobreaker defaults documented on
	// resilience.BreakerConfig.
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
}

// CircuitBreakerConfig mirrors resilience.BreakerConfig via viper tags.
type CircuitBreakerConfig struct {
	MaxFailures uint32 `mapstructure:"max_failures"`
	IntervalMS  int    `mapstructure:"interval_ms"`
	TimeoutMS   int    `mapstructure:"timeout_ms"`
	HalfOpenMax uint32 `mapstructure:"half_open_max"`
}

// ObservabilityConfig configures the optional Prometheus metrics endpoint
// and OpenTelemetry tracer. Both are opt-in — leaving them empty means
// the daemon runs with zero inbound HTTP surface (matching rousseau's
// default posture) and zero telemetry output.
type ObservabilityConfig struct {
	// MetricsAddr binds the Prometheus /metrics endpoint, e.g. ":9100".
	// Empty disables the endpoint entirely.
	MetricsAddr string `mapstructure:"metrics_addr"`
	// OTLPEndpoint is the base URL of an OTLP-HTTP collector, e.g.
	// "http://otel-collector:4318". Empty leaves the tracer noop.
	OTLPEndpoint string `mapstructure:"otlp_endpoint"`
	// AuditEgress configures the enterprise-only streaming
	// audit-log sink. Zero value leaves the sink Nop (no records
	// leave the process) — the OSS stdout+SQLite audit path is
	// unaffected. Requires FeatureAuditEgress on the licence.
	AuditEgress AuditEgressConfig `mapstructure:"audit_egress"`
}

// AuditEgressConfig is the operator-facing view of
// internal/observability/audit_egress. Kept as a separate
// config type so mapstructure tags don't leak into the sink
// package.
type AuditEgressConfig struct {
	// Kind selects the sink implementation. Empty (default)
	// leaves audit egress disabled; "otlp_http" ships the
	// OTLP/HTTP logs pilot.
	Kind string `mapstructure:"kind"`
	// Endpoint is the OTLP/HTTP logs URL, e.g.
	// "https://siem.example.com/v1/logs". Required when
	// kind = "otlp_http".
	Endpoint string `mapstructure:"endpoint"`
	// Headers are added to every request. Standard use: bearer
	// tokens, Splunk HEC "Authorization: Splunk <token>".
	Headers map[string]string `mapstructure:"headers"`
	// BatchSize caps records per push. Zero uses the sink's
	// documented default.
	BatchSize int `mapstructure:"batch_size"`
	// FlushInterval bounds how long a partial batch waits
	// before being pushed. Zero uses the sink default.
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	// QueueSize caps in-memory records; overflow drops oldest.
	// Zero uses the sink default.
	QueueSize int `mapstructure:"queue_size"`
	// HTTPTimeout caps a single push attempt. Zero uses the
	// sink default (10s).
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
	// Chained wraps the sink in a hash-chained tamper-evident
	// layer (see internal/observability/audit_egress.ChainedSink).
	// Compliance-officer visible feature; recommended for SOC 2 /
	// ISO 27001 / HIPAA audit-trail requirements.
	Chained bool `mapstructure:"chained"`
}

// SMSConfig configures the Twilio/Vonage SMS transport.
type SMSConfig struct {
	Provider    string `mapstructure:"provider"` // "twilio" or "vonage"
	From        string `mapstructure:"from"`
	AccountSID  string `mapstructure:"account_sid"` // Twilio
	AuthToken   string `mapstructure:"auth_token"`  // Twilio or Vonage secret
	APIKey      string `mapstructure:"api_key"`     // Vonage
	BaseURL     string `mapstructure:"base_url"`    // override for testing / regional endpoints
	ReplyHeader string `mapstructure:"reply_header"`
}

// IMessageConfig configures the BlueBubbles-backed iMessage transport.
type IMessageConfig struct {
	BaseURL      string `mapstructure:"base_url"`      // "http://localhost:1234"
	Password     string `mapstructure:"password"`      // BlueBubbles server password
	ChatGUID     string `mapstructure:"chat_guid"`     // outbound target
	PollInterval string `mapstructure:"poll_interval"` // duration string, e.g. "2s"
	ReplyHeader  string `mapstructure:"reply_header"`
}

// EmailConfig configures the IMAP+SMTP email transport.
type EmailConfig struct {
	IMAPAddr     string `mapstructure:"imap_addr"`
	IMAPUsername string `mapstructure:"imap_username"`
	IMAPPassword string `mapstructure:"imap_password"`
	Mailbox      string `mapstructure:"mailbox"`
	PollInterval string `mapstructure:"poll_interval"`

	SMTPAddr     string `mapstructure:"smtp_addr"`
	SMTPUsername string `mapstructure:"smtp_username"`
	SMTPPassword string `mapstructure:"smtp_password"`

	From        string `mapstructure:"from"`
	ReplyHeader string `mapstructure:"reply_header"`
}

// SlackConfig configures the Slack Socket Mode transport.
type SlackConfig struct {
	AppToken    string   `mapstructure:"app_token"`
	BotToken    string   `mapstructure:"bot_token"`
	BotUserID   string   `mapstructure:"bot_user_id"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
}

// DiscordConfig configures the Discord Gateway transport.
type DiscordConfig struct {
	Token       string   `mapstructure:"token"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
}

// MatrixConfig configures the Matrix client-server transport.
type MatrixConfig struct {
	HomeserverURL string   `mapstructure:"homeserver_url"`
	AccessToken   string   `mapstructure:"access_token"`
	UserID        string   `mapstructure:"user_id"`
	ReplyHeader   string   `mapstructure:"reply_header"`
	Allowlist     []string `mapstructure:"allowlist"`
}

// VertexConfig configures the Google Vertex AI provider (Anthropic on
// Vertex).
type VertexConfig struct {
	Project         string `mapstructure:"project"`
	Region          string `mapstructure:"region"`
	Model           string `mapstructure:"model"`
	CredentialsFile string `mapstructure:"credentials_file"`
	MaxTokens       int64  `mapstructure:"max_tokens"`
}

// BedrockConfig configures the AWS Bedrock provider.
type BedrockConfig struct {
	Region    string `mapstructure:"region"`
	Model     string `mapstructure:"model"`
	Profile   string `mapstructure:"profile"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// OpenAIConfig configures the OpenAI-compatible provider. Shared by
// openai / openrouter / ollama / other OpenAI-shim endpoints.
type OpenAIConfig struct {
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	BaseURL   string `mapstructure:"base_url"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// TelegramConfig configures the Telegram Bot API transport.
type TelegramConfig struct {
	Token       string   `mapstructure:"token"`
	BaseURL     string   `mapstructure:"base_url"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
}

// SignalConfig configures the signal-cli transport.
type SignalConfig struct {
	// Binary is the signal-cli executable. Empty defaults to "signal-cli".
	Binary string `mapstructure:"binary"`
	// Account is the E.164 phone number the daemon runs as.
	Account string `mapstructure:"account"`
	// ExtraArgs are inserted between `-a <account>` and `jsonRpc`.
	ExtraArgs []string `mapstructure:"extra_args"`
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string `mapstructure:"reply_header"`
	// Allowlist restricts inbound handling to these E.164 numbers.
	Allowlist []string `mapstructure:"allowlist"`
	// AttachmentsDir is the local path where signal-cli persists
	// received attachments (typically `<signal-cli-data>/attachments`).
	// Required when `media.audio.backend` is configured and the
	// operator wants voice notes routed through transcription.
	AttachmentsDir string `mapstructure:"attachments_dir"`
}

// WhatsAppConfig groups the whatsapp transport tuning knobs.
type WhatsAppConfig struct {
	// ReplyHeader is prepended to every outbound message. Empty uses
	// the built-in default ("💎 *Rousseau Agent*\n\n"). Set to a single
	// space " " to disable the prefix entirely.
	ReplyHeader string `mapstructure:"reply_header"`
	// Voice enables whisper-based transcription for inbound voice
	// notes. When disabled, audio messages are logged and skipped.
	Voice VoiceConfig `mapstructure:"voice"`
}

// VoiceConfig configures voice-note transcription.
type VoiceConfig struct {
	// Enabled toggles the whisper transcriber. Off by default because
	// it requires the whisper.cpp CLI to be installed.
	Enabled bool `mapstructure:"enabled"`
	// Binary is the whisper CLI to invoke. Empty defaults to "whisper".
	Binary string `mapstructure:"binary"`
	// Model is passed to --model (e.g. "base.en", "small").
	Model string `mapstructure:"model"`
	// ModelPath is an explicit .bin path; takes precedence over Model.
	ModelPath string `mapstructure:"model_path"`
	// Language is passed to --language. Empty auto-detects.
	Language string `mapstructure:"language"`
	// ExtraArgs are appended to every whisper invocation.
	ExtraArgs []string `mapstructure:"extra_args"`
}

// AnthropicConfig configures the direct Anthropic API provider.
type AnthropicConfig struct {
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// ClaudeCLIConfig configures the claudecli (subprocess) provider.
type ClaudeCLIConfig struct {
	// Binary is the claude executable. Empty defaults to "claude".
	Binary string `mapstructure:"binary"`
	// Model overrides claude's default.
	Model string `mapstructure:"model"`
	// PermissionMode is claude's --permission-mode
	// (acceptEdits, auto, bypassPermissions, default, dontAsk, plan).
	// Unattended daemons (whatsapp) generally need "bypassPermissions".
	PermissionMode string `mapstructure:"permission_mode"`
	// Bare passes --bare to claude, which skips CLAUDE.md auto-discovery,
	// hooks, LSP, plugin sync, auto-memory, background prefetches, and
	// keychain reads. Cuts per-invocation cold-start latency from minutes
	// (walking a large mounted workspace) to seconds. Trade-off:
	// authentication becomes strictly ANTHROPIC_API_KEY / apiKeyHelper
	// (no OAuth / keychain). Recommended for unattended bridge daemons
	// (whatsapp) where the workspace scan is pure overhead per message.
	Bare bool `mapstructure:"bare"`
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string `mapstructure:"extra_args"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// StateConfig configures the session store.
//
// Two drivers ship in the core: sqlite (default — single-replica,
// zero-config, embedded) and postgres (multi-replica HA, requires
// an external server). Selection lives here rather than a top-
// level driver field so operators bind one struct and forget it.
type StateConfig struct {
	// Driver selects the backend: "sqlite" (default) or "postgres".
	// Empty is treated as "sqlite" so existing configs keep working
	// with no change.
	Driver string `mapstructure:"driver"`
	// Path is the SQLite database path. Ignored when driver is
	// "postgres".
	Path string `mapstructure:"path"`
	// DSN is the Postgres libpq-style URL, e.g.
	//   postgres://user:pass@host:5432/rousseau?sslmode=require
	// Ignored when driver is "sqlite". Required when driver is
	// "postgres".
	DSN string `mapstructure:"dsn"`
}

// AgentConfig configures the agent loop.
type AgentConfig struct {
	SystemPrompt  string            `mapstructure:"system_prompt"`
	MaxIterations int               `mapstructure:"max_iterations"`
	Approver      ApproverConfig    `mapstructure:"approver"`
	Compression   CompressionConfig `mapstructure:"compression"`
	SkillsDir     string            `mapstructure:"skills_dir"`
	// SkillBundles configures the enterprise-only
	// cryptographically-signed skill bundle loader (see
	// internal/skills/bundle). Zero value leaves the loader
	// off — plain-markdown SkillsDir behaviour is unchanged.
	// Requires FeatureGovernanceAdvanced.
	SkillBundles SkillBundlesConfig `mapstructure:"skill_bundles"`
}

// SkillBundlesConfig is the operator-facing view of the
// signed-bundle loader. Zero value = disabled.
type SkillBundlesConfig struct {
	// Dir is the directory the loader scans for
	// *.skill.json files. Empty leaves the loader off.
	Dir string `mapstructure:"dir"`
	// TrustedPublisherKeys is the base64-encoded Ed25519
	// public key allow-list. A bundle whose
	// signature.public_key isn't in this list is rejected —
	// the trust root is the operator, not implicit.
	// Standard base64 (matches the same encoding the license
	// package uses for embedded keys).
	TrustedPublisherKeys []string `mapstructure:"trusted_publisher_keys"`
	// Strict, when true, elevates verification failures to
	// ERROR log level. Verification never falls through to
	// "load anyway" — a broken bundle simply isn't loaded.
	Strict bool `mapstructure:"strict"`
}

// CompressionConfig configures session compression. Disabled by
// default because a long-running daemon on a subscription-tier claude
// account rarely needs it — turn it on when running against
// pay-per-token providers.
type CompressionConfig struct {
	// Enabled toggles the LLM-backed compressor. When false, the
	// Agent uses NoopCompressor.
	Enabled bool `mapstructure:"enabled"`
	// TriggerMessages is the message count above which compression
	// engages. Zero uses the default (60).
	TriggerMessages int `mapstructure:"trigger_messages"`
	// KeepRecent is how many recent messages to preserve verbatim.
	// Zero uses the default (8).
	KeepRecent int `mapstructure:"keep_recent"`
	// Prompt overrides the default summarisation instruction.
	Prompt string `mapstructure:"prompt"`
}

// ApproverConfig picks and configures the tool-call approval policy.
//
// mode:
//   - "allow_all" (default): every tool call runs. Suitable when the
//     provider is claudecli, which handles its own approvals.
//   - "deny_all": block every tool call. Useful as a smoke test or
//     when running a read-only inspection session.
//   - "pattern":  applies Allow / Deny regex rules; deny wins over
//     allow; unmatched requests fall back to `default`.
type ApproverConfig struct {
	Mode    string         `mapstructure:"mode"`
	Reason  string         `mapstructure:"reason"`
	Default string         `mapstructure:"default"` // "allow" or "deny" for pattern mode
	Allow   []PatternEntry `mapstructure:"allow"`
	Deny    []PatternEntry `mapstructure:"deny"`
	// RBAC wraps the mode-selected approver with a group-based
	// gate. Zero value leaves RBAC off — the mode-selected
	// approver runs alone. Activates only when the licence
	// unlocks FeatureGovernanceAdvanced; without the licence the
	// rules are ignored and the daemon logs an INFO on boot.
	RBAC RBACConfig `mapstructure:"rbac"`
	// OPA wraps the approver chain (below RBAC) with a Rego-
	// per-tool-call policy. Zero value leaves OPA off. Same
	// licence gate as RBAC. Composition order:
	//   inner ← RBAC ← OPA
	// so both layers can veto; a broken policy falls back to
	// inner without wrapping.
	OPA OPAConfig `mapstructure:"opa"`
	// MultiParty wraps the approver chain (outermost) with an
	// N-approvers-required layer. Rules that name a tool block
	// its execution until N distinct SSO-authenticated
	// approvers reply /approve via chat. Zero value leaves the
	// layer off. Same licence gate. Composition order:
	//   inner ← RBAC ← OPA ← MultiParty
	// so a request must pass all three before the mode-selected
	// (pattern / TUI) approver has its final say.
	MultiParty MultiPartyConfig `mapstructure:"multi_party"`
}

// RBACConfig configures the group-based RBAC wrapper.
type RBACConfig struct {
	// Rules maps a tool name to the SSO groups permitted to
	// invoke it. A tool not listed here bypasses the RBAC layer
	// — only explicitly-locked tools filter. See
	// internal/agent/rbac for semantics.
	Rules []RBACRule `mapstructure:"rules"`
}

// OPAConfig configures the Rego-per-tool-call approver (see
// internal/agent/opa). Zero value leaves the OPA layer off —
// the mode-selected + RBAC-wrapped approver runs alone.
type OPAConfig struct {
	// PolicyFile is the on-disk path to the Rego module the
	// daemon evaluates on every tool call. Read at boot; a
	// missing file or parse error is fatal to the layer (WARN
	// + wrap-skipped, matching wrapWithRBAC's failure mode).
	PolicyFile string `mapstructure:"policy_file"`
	// Query overrides the default Rego query
	// (`data.rousseau.authz`). Rarely needed.
	Query string `mapstructure:"query"`
}

// MultiPartyConfig configures the N-approvers-required approver
// (see internal/agent/approval). Zero value leaves the layer
// off. Requires FeatureGovernanceAdvanced.
type MultiPartyConfig struct {
	// Rules list the tools that need multi-party approval.
	Rules []MultiPartyRule `mapstructure:"rules"`
}

// MultiPartyRule pins one tool to a minimum-approvers threshold
// + timeout.
type MultiPartyRule struct {
	// Tool is the model-facing tool name.
	Tool string `mapstructure:"tool"`
	// NeededApprovals is the count of DISTINCT non-self
	// approvers required to allow the call.
	NeededApprovals int `mapstructure:"needed_approvals"`
	// Timeout bounds how long the pending request lives. Zero
	// uses approval.DefaultTimeout (15m).
	Timeout time.Duration `mapstructure:"timeout"`
}

// RBACRule mirrors [rbac.Rule] but keeps mapstructure tags out
// of the domain package.
type RBACRule struct {
	Tool          string   `mapstructure:"tool"`
	AllowedGroups []string `mapstructure:"allowed_groups"`
}

// PatternEntry mirrors agent.PatternRule but decouples config from the
// agent package so importers don't need both.
type PatternEntry struct {
	Tool  string `mapstructure:"tool"`
	Match string `mapstructure:"match"`
}

// Load resolves configuration from CLI flags (via viper.BindPFlag in
// callers), environment variables, an optional YAML file at path
// (defaults to ~/.config/rousseau/config.yaml), and hard-coded defaults.
func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix("ROUSSEAU")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		v.Set("anthropic.api_key", key)
	}

	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".config", "rousseau", "config.yaml")
		}
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			var pathErr *os.PathError
			if !isNotExist(err, &pathErr) {
				return nil, fmt.Errorf("config: read %s: %w", path, err)
			}
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}
	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("provider", "claudecli")
	v.SetDefault("anthropic.model", "claude-sonnet-4-6")
	v.SetDefault("anthropic.max_tokens", 4096)
	v.SetDefault("claudecli.binary", "claude")
	// Explicit default so viper.AutomaticEnv picks up ROUSSEAU_CLAUDECLI_BARE
	// (viper only checks env for keys it knows about via SetDefault/BindEnv).
	v.SetDefault("claudecli.bare", false)
	v.SetDefault("openrouter.base_url", "https://openrouter.ai/api/v1")
	v.SetDefault("ollama.base_url", "http://localhost:11434/v1")
	v.SetDefault("ollama.api_key", "not-required")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("agent.max_iterations", 32)
	home, err := os.UserHomeDir()
	if err == nil {
		v.SetDefault("state.path", filepath.Join(home, ".local", "share", "rousseau", "sessions.db"))
	}
}

func isNotExist(err error, out **os.PathError) bool {
	if os.IsNotExist(err) {
		return true
	}
	if err != nil && strings.Contains(err.Error(), "Config File") && strings.Contains(err.Error(), "Not Found") {
		return true
	}
	_ = out
	return false
}
