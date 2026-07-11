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
	// Provider selects the LLM backend: "claudecli" (default — shells
	// out to the local `claude` CLI, inheriting its auth) or
	// "anthropic" (direct API, requires ANTHROPIC_API_KEY).
	Provider  string          `mapstructure:"provider"`
	Anthropic AnthropicConfig `mapstructure:"anthropic"`
	ClaudeCLI ClaudeCLIConfig `mapstructure:"claudecli"`
	Log       LogConfig       `mapstructure:"log"`
	State     StateConfig     `mapstructure:"state"`
	Agent     AgentConfig     `mapstructure:"agent"`
	WhatsApp  WhatsAppConfig  `mapstructure:"whatsapp"`
}

// WhatsAppConfig configures the whatsapp transport.
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
	SystemPrompt  string         `mapstructure:"system_prompt"`
	MaxIterations int            `mapstructure:"max_iterations"`
	Approver      ApproverConfig `mapstructure:"approver"`
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
