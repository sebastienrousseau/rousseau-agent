// Command rousseau is the entry point for the rousseau-agent binary. It
// wires signal handling and hands off to internal/cli.Execute.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sebastienrousseau/rousseau-agent/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(cli.Execute(ctx))
}
