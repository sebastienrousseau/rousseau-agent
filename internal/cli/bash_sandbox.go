package cli

import (
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// buildFSGuard turns tools.fs into the guard every file tool shares.
func buildFSGuard(cfg config.FSConfig) (*fsguard.Guard, error) {
	g, err := fsguard.New(cfg.Root, cfg.Deny)
	if err != nil {
		return nil, fmt.Errorf("cli: tools.fs: %w", err)
	}
	return g, nil
}

// registerFileTools registers read, write, edit and grep bound to one
// guard. Both the daemon and the chat TUI go through here so the
// deny list and workspace root apply identically.
func registerFileTools(registry *tools.Registry, g *fsguard.Guard) {
	rt := builtin.NewReadTool()
	rt.Guard = g
	wt := builtin.NewWriteTool()
	wt.Guard = g
	et := builtin.NewEditTool()
	et.Guard = g
	gt := builtin.NewGrepTool(0, 0)
	gt.Guard = g
	registry.MustRegister(rt)
	registry.MustRegister(wt)
	registry.MustRegister(et)
	registry.MustRegister(gt)
}

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
	tool := builtin.NewBashToolWithSandbox(timeout, backend)
	tool.EnvPassthrough = cfg.EnvPassthrough
	return tool, nil
}

// requireSandboxPolicy refuses to run an unattended daemon whose bash
// tool executes with no isolation unless the operator opted in. An
// allowlisted chat message can drive bash; without a sandbox that is
// a shell on the host with the daemon's privileges, which should be a
// deliberate choice, not the silent default.
func requireSandboxPolicy(cfg config.BashConfig, transportName string) error {
	kind := cfg.Sandbox.Kind
	if kind != "" && kind != "none" {
		return nil
	}
	if cfg.Sandbox.AllowUnsandboxed {
		return nil
	}
	return fmt.Errorf("%s: tools.bash.sandbox.kind is unset, so the bash tool would run commands "+
		"directly on the host. Set tools.bash.sandbox.kind to \"nsjail\" or \"gvisor\", or set "+
		"tools.bash.sandbox.allow_unsandboxed: true to accept host execution explicitly",
		transportName)
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
