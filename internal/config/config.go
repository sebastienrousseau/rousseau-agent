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
	Anthropic AnthropicConfig `mapstructure:"anthropic"`
	Log       LogConfig       `mapstructure:"log"`
	State     StateConfig     `mapstructure:"state"`
	Agent     AgentConfig     `mapstructure:"agent"`
}

// AnthropicConfig configures the Claude provider.
type AnthropicConfig struct {
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	MaxTokens int64  `mapstructure:"max_tokens"`
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
	SystemPrompt  string `mapstructure:"system_prompt"`
	MaxIterations int    `mapstructure:"max_iterations"`
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
	v.SetDefault("anthropic.model", "claude-sonnet-4-6")
	v.SetDefault("anthropic.max_tokens", 4096)
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
