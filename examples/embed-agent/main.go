// Package main demonstrates embedding the rousseau agent loop in your
// own program. Run with:
//
//	go run ./examples/embed-agent
//
// The claudecli provider requires the `claude` CLI on $PATH. To use the
// direct Anthropic API instead, swap the provider construction for
// anthropic.New(anthropic.Config{APIKey: os.Getenv("ANTHROPIC_API_KEY")}).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/claudecli"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/builtin"
)

func main() { os.Exit(run(context.Background(), defaultProvider(), os.Stdout, os.Stderr)) }

// defaultProvider shells out to the local `claude` CLI. It inherits
// Claude Code's authentication — no ANTHROPIC_API_KEY handling
// required. permission_mode=acceptEdits auto-approves file edits while
// still gating shell commands.
func defaultProvider() agent.Provider {
	return claudecli.New(claudecli.Config{PermissionMode: "acceptEdits"})
}

// run drives one conversation turn against provider and returns the
// process exit code. The provider is a parameter rather than a local so
// that the loop can be pointed at any agent.Provider — anthropic,
// bedrock, vertex, openai — without touching the rest of the program.
// main does nothing but call os.Exit so that tests can drive run.
func run(ctx context.Context, provider agent.Provider, out, errOut io.Writer) int {
	// Register the tools you want the model to reach for. The claudecli
	// provider handles tools inside the subprocess so registration is
	// currently a no-op for that provider; use it with the anthropic
	// provider to exercise the tool loop directly.
	registry := tools.NewRegistry()
	registry.MustRegister(builtin.NewReadTool())
	registry.MustRegister(builtin.NewGrepTool(0, 0))

	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ag := agent.New(provider, registry, logger, agent.Options{
		SystemPrompt: "You are a careful, concise coding assistant.",
	})

	// A Session is the unit of conversation continuity. Its UUID is
	// threaded through to claude via --session-id so that subsequent
	// turns resume the same context.
	session := agent.NewSession("embed-example")
	session.Append(agent.NewUserText("Reply with EXACTLY the word 'ready'."))

	// Turn advances the conversation by one round-trip. Tool calls, if
	// any, are dispatched to the Registry and their results appended to
	// the session before the loop continues.
	reply, err := ag.Turn(ctx, session)
	if err != nil {
		fmt.Fprintf(errOut, "turn: %v\n", err)
		return 1
	}

	// The final assistant message is always the last one in the session
	// and is also returned by Turn.
	fmt.Fprintf(out, "assistant: %s\n", reply.Content[0].Text)
	return 0
}
