package cli

import (
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// defaultBashTimeout is the fallback when config.Bash.TimeoutSeconds
// is zero. Matches the pre-config default in builtin.NewBashTool.
const defaultBashTimeout = 60 * time.Second

// buildBashTool constructs the bash built-in tool from config. Two
// code paths merge here so both `whatsapp` (daemon) and `chat` (TUI)
// pick up sandbox config from the same yaml:
//
//	tools:
//	  bash:
//	    timeout_seconds: 30
//	    sandbox:
//	      kind: nsjail
//	      no_network: true
//	      cpu_seconds: 10
//	      memory_mb: 256
//	      readonly: [/usr, /lib]
//	      writable: [/workspace]
//
// The pre-sandbox behaviour is preserved: an empty ToolsConfig
// (which is what every existing deployment has) produces the same
// tool the previous `builtin.NewBashTool(60*time.Second)` did.
func buildBashTool(cfg config.BashConfig) (*builtin.BashTool, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = defaultBashTimeout
	}
	backend, err := buildBashSandbox(cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		return builtin.NewBashTool(timeout), nil
	}
	return builtin.NewBashToolWithSandbox(timeout, backend), nil
}

// buildBashSandbox turns the config into a sandbox.Backend. Returns
// (nil, nil) for the "no isolation" case so buildBashTool can short-
// circuit to the direct-exec constructor.
func buildBashSandbox(cfg config.BashSandboxConfig) (sandbox.Backend, error) {
	if cfg.Kind == "" || cfg.Kind == "none" {
		return nil, nil
	}
	return sandbox.NewWithPolicy(cfg.Kind, resolveSandboxPolicy(cfg))
}

// resolveSandboxPolicy converts BashSandboxConfig into sandbox.Policy,
// applying the "safe by default" rule for NoNetwork: unset (nil
// pointer) means "on" when the backend is one that supports
// isolation. Callers who legitimately need network from bash inside
// the sandbox set `no_network: false` explicitly.
func resolveSandboxPolicy(cfg config.BashSandboxConfig) sandbox.Policy {
	noNetwork := true // safe default
	if cfg.NoNetwork != nil {
		noNetwork = *cfg.NoNetwork
	}
	return sandbox.Policy{
		NoNetwork:   noNetwork,
		TmpdirRoot:  cfg.TmpdirRoot,
		Wallclock:   time.Duration(cfg.WallclockSeconds) * time.Second,
		CPUSeconds:  cfg.CPUSeconds,
		MemoryBytes: int64(cfg.MemoryMB) * 1024 * 1024,
		Readonly:    cfg.Readonly,
		Writable:    cfg.Writable,
	}
}
