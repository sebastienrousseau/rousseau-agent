package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// daemonFSRoot is the workspace root the daemon's file tools use: the
// configured tools.fs.root, or $XDG_DATA_HOME/rousseau/workspace when
// unset, so a prompt-injected read or write cannot reach the rest of
// $HOME by default. tools.fs.root: "/" is the explicit opt-out. The
// directory is not created here; the write tool creates parents.
func daemonFSRoot(cfg config.FSConfig) string {
	if cfg.Root != "" {
		return cfg.Root
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "rousseau", "workspace")
}

// buildFSGuard turns tools.fs into the guard every file tool shares.
// A configured audit chain key file is denied as well: a tool that
// could read it could forge a chain that verifies. (The generated
// default key lives under the state dir, which the default deny list
// covers.)
func buildFSGuard(cfg config.FSConfig, ae config.AuditEgressConfig) (*fsguard.Guard, error) {
	deny := cfg.Deny
	if ae.ChainHMACKeyFile != "" {
		p, err := filepath.Abs(ae.ChainHMACKeyFile)
		if err != nil {
			return nil, fmt.Errorf("cli: chain key path: %w", err)
		}
		deny = append(append([]string(nil), deny...), p)
	}
	g, err := fsguard.New(cfg.Root, deny)
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

// buildDaemonToolRegistry assembles the daemon's tool registry: the
// guarded file tools, the (policy-checked) bash tool, spawn_subagent
// and every enabled integration suite. Split out of assembleDaemon
// so the daemon constructor stays a sequence of subsystem builders.
func buildDaemonToolRegistry(opts *Options) (*tools.Registry, error) {
	cfg := opts.Config
	registry := tools.NewRegistry()
	fs := cfg.Tools.FS
	fs.Root = daemonFSRoot(fs)
	guard, err := buildFSGuard(fs, cfg.Observability.AuditEgress)
	if err != nil {
		return nil, err
	}
	opts.Logger.Info("tools.fs.root", "root", guard.Root())
	registerFileTools(registry, guard)
	if err := requireSandboxPolicy(cfg.Tools.Bash, "daemon"); err != nil {
		return nil, err
	}
	bash, err := buildBashTool(cfg.Tools.Bash)
	if err != nil {
		return nil, fmt.Errorf("cli: build bash tool: %w", err)
	}
	registry.MustRegister(bash)
	// spawn_subagent exposes the sub-agent parallelism primitive
	// (subagent.Spawn) to the model. Zero-value Policy uses the
	// defaults documented on subagent.Policy (MaxConcurrent=4,
	// PerTaskTimeout=5m, no aggregate token budget). Operators wanting
	// tighter limits can pass a non-zero Policy here.
	registry.MustRegister(builtin.NewSpawnSubagentTool(subagent.Policy{}))

	// Register every enabled tool-integration suite. Each suite is
	// opt-in via the integrations block in the config; a nil
	// integrations config leaves the registry unchanged.
	if err := integrations.RegisterAll(registry, integrationsFromConfig(cfg), opts.Logger); err != nil {
		return nil, err
	}
	return registry, nil
}

// requireSandboxPolicy refuses to run an unattended daemon whose bash
// tool executes with no isolation unless the operator opted in. An
// allowlisted chat message can drive bash; without a sandbox that is
// a shell on the host with the daemon's privileges, which should be a
// deliberate choice, not the silent default.
//
// gvisor counts as a sandbox only while it confines the filesystem:
// a mount that is "/", $HOME or an ancestor of $HOME puts the secrets
// bash must not read back inside the sandbox, so it is refused here
// regardless of allow_unsandboxed (an opt-in for kind none, not for a
// sandbox configured to expose the host).
func requireSandboxPolicy(cfg config.BashConfig, transportName string) error {
	kind := cfg.Sandbox.Kind
	if kind == "gvisor" {
		if err := sandbox.CheckFilesystemConfinement(resolveSandboxPolicy(cfg.Sandbox)); err != nil {
			return fmt.Errorf("%s: tools.bash.sandbox.kind gvisor does not confine the filesystem: %w. "+
				"Remove that path from tools.bash.sandbox.readonly/writable; mount only the workspace",
				transportName, err)
		}
		return nil
	}
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
