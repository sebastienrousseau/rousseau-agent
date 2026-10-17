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
	A2A           A2AConfig           `mapstructure:"a2a"`
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

	path, err := readConfigFile(v, path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := decodeConfig(v, cfg, path); err != nil {
		return nil, err
	}
	if err := restoreEnvKeyCase(cfg, path); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if err := expandEnvRefs(cfg); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// readConfigFile reads path into v and returns the path actually
// used. Only the implicit default path may be absent. An operator who
// named a file that does not exist gets an error, not a daemon
// silently running on defaults (allow-all approver, no governance).
func readConfigFile(v *viper.Viper, path string) (string, error) {
	explicit := path != ""
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".config", "rousseau", "config.yaml")
		}
	}
	if path == "" {
		return path, nil
	}
	v.SetConfigFile(path)
	err := v.ReadInConfig()
	if err == nil {
		return path, nil
	}
	var pathErr *os.PathError
	if !isNotExist(err, &pathErr) {
		return path, fmt.Errorf("config: read %s: %w", path, err)
	}
	if explicit {
		return path, fmt.Errorf("config: %s does not exist (given via --config); create it or drop the flag to use the default path", path)
	}
	return path, nil
}

// decodeConfig unmarshals v into cfg. Strict by default: a key that
// matches no field is an error. A misspelt "aprover:" used to be
// ignored silently, leaving the default allow-all policy in force
// with nothing in the logs. ROUSSEAU_CONFIG_ALLOW_UNKNOWN=1 restores
// the lenient decode for running an older binary against a newer
// config file.
func decodeConfig(v *viper.Viper, cfg *Config, path string) error {
	if os.Getenv(envAllowUnknownKeys) == "1" {
		if err := v.Unmarshal(cfg); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		return nil
	}
	if err := v.UnmarshalExact(cfg); err != nil {
		return fmt.Errorf("config: %s: %w (fix or remove the key; set %s=1 to ignore unknown keys)",
			path, err, envAllowUnknownKeys)
	}
	return nil
}

// envAllowUnknownKeys opts out of strict config decoding.
const envAllowUnknownKeys = "ROUSSEAU_CONFIG_ALLOW_UNKNOWN"

func setDefaults(v *viper.Viper) {
	v.SetDefault("provider", "claudecli")
	v.SetDefault("anthropic.model", "claude-sonnet-4-6")
	v.SetDefault("anthropic.max_tokens", 4096)
	v.SetDefault("claudecli.binary", "claude")
	// Explicit default so viper.AutomaticEnv picks up ROUSSEAU_CLAUDECLI_BARE
	// (viper only checks env for keys it knows about via SetDefault/BindEnv).
	v.SetDefault("claudecli.bare", false)
	// Bound explicitly (no default) so ROUSSEAU_CLAUDECLI_PERMISSION_MODE
	// works; unattended transports refuse to start without a mode.
	_ = v.BindEnv("claudecli.permission_mode") //nolint:errcheck // BindEnv only errors on zero args
	v.SetDefault("openrouter.base_url", "https://openrouter.ai/api/v1")
	v.SetDefault("ollama.base_url", "http://localhost:11434/v1")
	v.SetDefault("ollama.api_key", "not-required")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("agent.max_iterations", 32)
	v.SetDefault("agent.session_idle_timeout", "12h")
	v.SetDefault("agent.turn_timeout", "30m")
	v.SetDefault("agent.tool_timeout", "10m")
	v.SetDefault("agent.max_concurrent_turns", 4)
	// Tamper evidence is the point of an audit trail; an operator who
	// enables egress gets the hash chain unless they turn it off.
	v.SetDefault("observability.audit_egress.chained", true)
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
