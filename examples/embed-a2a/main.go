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
	"fmt"
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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	s, err := server.New(a2a.CapabilityCard{
		AgentID: "example-peer",
		Name:    "example-peer",
		Version: "v0.0.2",
		Skills: []a2a.SkillDescriptor{
			{Name: "echo", Description: "returns the prompt verbatim"},
		},
	}, &echoHandler{logger: logger}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}

	// Grab an ephemeral port so this example doesn't collide with
	// anything else the operator has running.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	base := "http://" + ln.Addr().String()
	go func() {
		if err := (&http.Server{Handler: s.Router(), ReadHeaderTimeout: 5 * time.Second}).Serve(ln); err != nil &&
			err != http.ErrServerClosed {
			logger.Error("a2a.serve", "err", err.Error())
		}
	}()

	c, err := client.New(client.Config{Name: "self", Endpoint: base})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	card, err := c.FetchCard(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch card:", err)
		os.Exit(1)
	}
	fmt.Printf("peer card: %s (%s), streaming=%v\n", card.Name, card.Version, card.SupportsStreaming)

	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "hello world"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "submit:", err)
		os.Exit(1)
	}
	for upd := range ch {
		fmt.Printf("update: status=%s message=%q output=%q\n",
			upd.Status, upd.Message, upd.OutputText)
	}
}
