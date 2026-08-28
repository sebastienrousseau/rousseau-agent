// Package main demonstrates a self-contained A2A peer: it stands up
// an A2A server that echoes any prompt back with a completed status,
// then submits a task to itself and prints the update stream.
//
// Run with:
//
//	go run ./examples/embed-a2a
//
// A real deployment would set the CapabilityCard fields properly,
// pass a bearer-token allowlist in `New(_, _, auth)`, and back the
// Handler with an actual agent.Turn.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/server"
)

type echoHandler struct{ logger *slog.Logger }

func (h *echoHandler) OnTask(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	h.logger.Info("a2a.task.received", "task_id", task.TaskID, "prompt", task.Prompt)
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "thinking"})
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "echo: " + task.Prompt})
	return nil
}

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run executes the demo and returns the process exit code. main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out, errOut); err != nil {
		fmt.Fprintln(errOut, "embed-a2a:", err)
		return 1
	}
	return 0
}

// demo stands the peer up on an ephemeral loopback port and then talks
// to it.
func demo(ctx context.Context, out, errOut io.Writer) error {
	logger := slog.New(slog.NewTextHandler(errOut, nil))
	s, err := server.New(a2a.CapabilityCard{
		AgentID: "example-peer",
		Name:    "example-peer",
		Version: "v0.0.2",
		Skills: []a2a.SkillDescriptor{
			{Name: "echo", Description: "returns the prompt verbatim"},
		},
	}, &echoHandler{logger: logger}, nil)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	// Grab an ephemeral port so this example doesn't collide with
	// anything else the operator has running.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	httpSrv := &http.Server{Handler: s.Router(), ReadHeaderTimeout: 5 * time.Second}
	defer func() { _ = httpSrv.Close() }()
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("a2a.serve", "err", err.Error())
		}
	}()

	return probe(ctx, out, "http://"+ln.Addr().String())
}

// probe fetches the peer's capability card, submits one task and
// prints every streamed update until the terminal status arrives.
func probe(ctx context.Context, out io.Writer, endpoint string) error {
	c, err := client.New(client.Config{Name: "self", Endpoint: endpoint})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	card, err := c.FetchCard(ctx)
	if err != nil {
		return fmt.Errorf("fetch card: %w", err)
	}
	fmt.Fprintf(out, "peer card: %s (%s), streaming=%v\n", card.Name, card.Version, card.SupportsStreaming)

	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "hello world"})
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	for upd := range ch {
		fmt.Fprintf(out, "update: status=%s message=%q output=%q\n",
			upd.Status, upd.Message, upd.OutputText)
	}
	return nil
}
