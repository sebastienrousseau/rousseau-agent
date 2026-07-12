// Package config resolves runtime configuration with precedence
// flag > env > file > default. Callers wire it via Load.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Provider   string          `mapstructure:"provider"`
	Anthropic  AnthropicConfig `mapstructure:"anthropic"`
	ClaudeCLI  ClaudeCLIConfig `mapstructure:"claudecli"`
	OpenAI     OpenAIConfig    `mapstructure:"openai"`
	OpenRouter OpenAIConfig    `mapstructure:"openrouter"`
	Ollama     OpenAIConfig    `mapstructure:"ollama"`
	Log        LogConfig       `mapstructure:"log"`
	State      StateConfig     `mapstructure:"state"`
	Agent      AgentConfig     `mapstructure:"agent"`
	WhatsApp   WhatsAppConfig  `mapstructure:"whatsapp"`
	Signal     SignalConfig    `mapstructure:"signal"`
	Telegram   TelegramConfig  `mapstructure:"telegram"`
	Bedrock    BedrockConfig   `mapstructure:"bedrock"`
	Vertex     VertexConfig    `mapstructure:"vertex"`
	Matrix     MatrixConfig    `mapstructure:"matrix"`
	Slack      SlackConfig     `mapstructure:"slack"`
	Discord    DiscordConfig   `mapstructure:"discord"`
	SMS        SMSConfig       `mapstructure:"sms"`
	IMessage   IMessageConfig  `mapstructure:"imessage"`
	Email      EmailConfig     `mapstructure:"email"`
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
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string `mapstructure:"extra_args"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// StateConfig configures the session store.
type StateConfig struct {
	Path string `mapstructure:"path"`
}

// AgentConfig configures the agent loop.
type AgentConfig struct {
	SystemPrompt  string            `mapstructure:"system_prompt"`
	MaxIterations int               `mapstructure:"max_iterations"`
	Approver      ApproverConfig    `mapstructure:"approver"`
	Compression   CompressionConfig `mapstructure:"compression"`
	SkillsDir     string            `mapstructure:"skills_dir"`
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
