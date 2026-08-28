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

func main() { os.Exit(run()) }

// run installs the interrupt handler, runs the Cobra command tree and
// returns the process exit code. It exists so tests can drive the real
// entry point: main itself may do nothing but call os.Exit, which would
// take the test binary down with it.
func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return cli.Execute(ctx)
}
