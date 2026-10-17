package config

// ToolsConfig groups per-built-in-tool configuration. Today only
// `bash` has knobs (sandbox selection); more tools may follow.
type ToolsConfig struct {
	Bash BashConfig `mapstructure:"bash"`
	FS   FSConfig   `mapstructure:"fs"`
}

// FSConfig bounds the file tools (read, write, edit, grep). See
// internal/tools/fsguard for the rules.
type FSConfig struct {
	// Root confines every file tool to this directory and below. In
	// the daemon, empty means $XDG_DATA_HOME/rousseau/workspace; "/"
	// is the explicit opt-out (any path not on the deny list). The
	// interactive `rousseau chat` keeps empty as unrestricted.
	Root string `mapstructure:"root"`
	// Deny adds absolute paths to the built-in deny list (the
	// daemon's own config and state, SSH / GPG / cloud credentials,
	// /proc, /sys, /dev).
	Deny []string `mapstructure:"deny"`
}

// BashConfig configures the bash built-in tool. Zero value is the
// pre-sandbox behaviour: direct exec, no isolation.
type BashConfig struct {
	// TimeoutSeconds caps a single command. Zero uses 60s.
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// EnvPassthrough names extra environment variables (or "NAME*"
	// prefixes) commands may inherit from the daemon on top of the
	// scrubbed baseline (PATH, HOME, locale, TERM, TMPDIR, …).
	// Provider, licence and transport secrets are never passed.
	EnvPassthrough []string `mapstructure:"env_passthrough"`
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
	// AllowUnsandboxed is the explicit opt-in an unattended daemon
	// needs to run bash with Kind "none". Without it the daemon
	// refuses to start, the same way it refuses an unset
	// claudecli.permission_mode. The chat TUI is unaffected.
	AllowUnsandboxed bool `mapstructure:"allow_unsandboxed"`
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
//	      fail_closed: true   # deny when the hook errors or times out
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
	// FailClosed denies the operation when this hook errors or times
	// out. Default false keeps the fail-open behaviour.
	FailClosed bool `mapstructure:"fail_closed"`
}
