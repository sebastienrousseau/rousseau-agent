// Package main demonstrates cross-vendor A2A v1.0.1 interop by
// standing up a rousseau A2A server and calling it with the
// spec-conformant v1 client — the exact wire format any peer built
// against the A2A specification will send.
//
// The v0-shorthand `examples/embed-a2a` demo is preserved separately
// so operators can see the legacy shape; this one is the "federated
// with an arbitrary A2A vendor" demo.
//
// Run with:
//
//	go run ./examples/embed-a2a-federated
//
// Output shows:
//   - v1.0 well-known AgentCard fetch (`/.well-known/agent-card.json`)
//   - v1.0 spec-shaped message submission (`POST /message:send`)
//   - v1.0 SSE re-subscribe with `TASK_STATE_*` ProtoJSON enum values
//   - v1.0 colon-verb cancel (`POST /tasks/{id}:cancel`) on a second task
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

// echoHandler is a trivial handler that emits a "thinking" update
// followed by a completed update. Any real agent would drop its
// [agent.Turn] pipeline in here.
type echoHandler struct{ logger *slog.Logger }

func (h *echoHandler) OnTask(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	h.logger.Info("a2a.task.received", "task_id", task.TaskID, "prompt", task.Prompt)
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "thinking"})
	// A cross-vendor demo doesn't need to actually do work — just
	// echo the prompt back on the terminal frame.
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusCompleted, OutputText: "echo: " + task.Prompt})
	return nil
}

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run keeps main tiny so the tests can drive it directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out, errOut); err != nil {
		fmt.Fprintln(errOut, "embed-a2a-federated:", err)
		return 1
	}
	return 0
}

// demo brings the peer up on an ephemeral port and drives the four
// spec surfaces from a v1 client.
func demo(ctx context.Context, out, errOut io.Writer) error {
	logger := slog.New(slog.NewTextHandler(errOut, nil))
	s, err := server.New(a2a.CapabilityCard{
		AgentID: "federated-peer",
		Name:    "federated-peer",
		Version: "v0.0.4",
		Skills: []a2a.SkillDescriptor{
			{Name: "echo", Description: "returns the prompt verbatim", Tags: []string{"demo"}},
		},
		SupportsStreaming: true,
	}, &echoHandler{logger: logger}, nil)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
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

// probe drives the four spec surfaces (card / send / subscribe /
// cancel) so an operator running the example can eyeball each one.
func probe(ctx context.Context, out io.Writer, endpoint string) error {
	c, err := client.New(client.Config{Name: "self-federated", Endpoint: endpoint, Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// 1. v1.0 well-known AgentCard.
	card, err := c.GetAgentCard(ctx)
	if err != nil {
		return fmt.Errorf("get agent card: %w", err)
	}
	fmt.Fprintf(out, "agent card: name=%s version=%s protocol=%s streaming=%v skills=%d\n",
		card.Name, card.Version, card.ProtocolVersion, card.Capabilities.Streaming, len(card.Skills))
	for _, iface := range card.Interfaces {
		fmt.Fprintf(out, "  interface: %s @ %s (protocolVersion=%s)\n",
			iface.ProtocolBinding, iface.URL, iface.ProtocolVersion)
	}

	// 2. v1.0 message send.
	task, err := c.SendMessage(ctx, a2a.TextMessage("hello federated world"))
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	fmt.Fprintf(out, "submitted: id=%s state=%s\n", task.ID, task.Status.State)

	// 3. v1.0 subscribe (SSE re-subscribe with StreamResponse frames).
	stream, err := c.SubscribeToTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	for frame := range stream {
		if frame.StatusUpdate != nil {
			fmt.Fprintf(out, "stream: kind=%s state=%s\n",
				frame.StatusUpdate.Kind, frame.StatusUpdate.Status.State)
		}
	}

	// 4. v1.0 colon-verb cancel on a second task, to exercise the
	//    :cancel pattern on the wire.
	other, err := c.SendMessage(ctx, a2a.TextMessage("cancel me"))
	if err != nil {
		return fmt.Errorf("submit for cancel: %w", err)
	}
	if err := c.CancelTask(ctx, other.ID); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	fmt.Fprintf(out, "cancelled: id=%s\n", other.ID)

	return nil
}
