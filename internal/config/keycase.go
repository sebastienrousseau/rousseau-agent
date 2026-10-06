package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// restoreEnvKeyCase re-reads the config file with a case-preserving
// YAML decoder and swaps the environment maps viper lower-cased back
// to their original spelling.
//
// viper lower-cases every map key on read, including the values of
// map[string]string fields, so `GITHUB_PERSONAL_ACCESS_TOKEN: x`
// under mcp.clients.<name>.env reached the server as
// github_personal_access_token=x, and hook env entries suffered the
// same. Only the env maps are restored: every other key is a field
// name mapstructure matches case-insensitively anyway.
func restoreEnvKeyCase(cfg *Config, path string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-provided config path, already read by viper
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc struct {
		MCP struct {
			Clients map[string]struct {
				Env map[string]string `yaml:"env"`
			} `yaml:"clients"`
		} `yaml:"mcp"`
		Hooks map[string][]struct {
			Name string            `yaml:"name"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"hooks"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// viper already accepted the file; a shape this decoder cannot
		// read (e.g. hooks as a non-list) is not a config error.
		return nil //nolint:nilerr // best-effort restoration
	}
	for name, c := range doc.MCP.Clients {
		if client, ok := cfg.MCP.Clients[name]; ok && len(c.Env) > 0 {
			client.Env = c.Env
			cfg.MCP.Clients[name] = client
		}
	}
	restoreHookEnv(cfg.Hooks.PreToolUse, doc.Hooks["pre_tool_use"])
	restoreHookEnv(cfg.Hooks.PostToolUse, doc.Hooks["post_tool_use"])
	restoreHookEnv(cfg.Hooks.PreTurn, doc.Hooks["pre_turn"])
	restoreHookEnv(cfg.Hooks.PostTurn, doc.Hooks["post_turn"])
	restoreHookEnv(cfg.Hooks.OnError, doc.Hooks["on_error"])
	return nil
}

func restoreHookEnv(hooks []HookConfig, raw []struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
}) {
	for i := range hooks {
		for _, r := range raw {
			if r.Name == hooks[i].Name && len(r.Env) > 0 {
				hooks[i].Env = r.Env
			}
		}
	}
}
