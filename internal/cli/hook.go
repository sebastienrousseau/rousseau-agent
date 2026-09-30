package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/toolgate"
)

// Policy-bridge timing. claude treats a hook that times out as a
// non-blocking error and runs the tool, so rousseau must always answer
// first: decision < hook client < claude's per-hook limit.
const (
	toolgateDecisionTimeout = 580 * time.Second
	toolgateHookTimeout     = 590 * time.Second
	toolgateClaudeTimeout   = 600 // seconds, claude's hook "timeout"
)

// newHookCmd is the command claude runs as a PreToolUse hook when the
// daemon bridges its tool calls into rousseau's policy (see toolgate).
// Hidden: it is an internal protocol, not an operator surface.
//
// It must only ever exit 0 (allow) or 2 (block). claude treats any
// other status as a non-blocking hook error and runs the tool, so this
// command skips config loading (a broken config must not open the
// gate) and turns every outcome into one of those two codes.
func newHookCmd() *cobra.Command {
	hook := &cobra.Command{
		Use:    "hook",
		Short:  "Internal: policy hooks invoked by provider subprocesses",
		Hidden: true,
		// Replaces the root's config loading; see above.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	var socket string
	pre := &cobra.Command{
		Use:           "pre-tool-use",
		Short:         "Ask the daemon's policy whether a tool call may run (claude PreToolUse hook)",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			code := toolgate.RunHook(cmd.InOrStdin(), cmd.ErrOrStderr(), socket, toolgateHookTimeout)
			if code == toolgate.ExitAllow {
				return nil
			}
			return silentExit(toolgate.ExitBlock)
		},
	}
	pre.Flags().StringVar(&socket, "socket", "", "policy socket path (set by the daemon)")
	hook.AddCommand(pre)
	return hook
}
