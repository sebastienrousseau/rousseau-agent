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
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

func main() {
	// The claudecli provider shells out to the local `claude` CLI. It
	// inherits Claude Code's authentication — no ANTHROPIC_API_KEY
	// handling required. permission_mode=acceptEdits auto-approves file
	// edits while still gating shell commands.
	provider := claudecli.New(claudecli.Config{
		PermissionMode: "acceptEdits",
	})

	// Register the tools you want the model to reach for. The claudecli
	// provider handles tools inside the subprocess so registration is
	// currently a no-op for that provider; use it with the anthropic
	// provider to exercise the tool loop directly.
	registry := tools.NewRegistry()
	registry.MustRegister(builtin.NewReadTool())
	registry.MustRegister(builtin.NewGrepTool(0, 0))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	reply, err := ag.Turn(context.Background(), session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turn: %v\n", err)
		os.Exit(1)
	}

	// The final assistant message is always the last one in the session
	// and is also returned by Turn.
	fmt.Printf("assistant: %s\n", reply.Content[0].Text) //nolint:forbidigo // example program; stdout is the intended sink
}
