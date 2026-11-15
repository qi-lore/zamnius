package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// RunWithSignals is the process binding that main calls: it binds SIGINT and
// SIGTERM to the command's context, so an interruption stops the engine at an
// attempt boundary — the lease is released and the run is left `paused`, resumable —
// instead of killing the process mid-write (contracts/cli.md §3, FR-008).
//
// A first signal cancels the context; the command unwinds and returns exit `7`
// through the ordinary classification of a cancelled run. A second signal ends the
// process immediately with exit `1`, because an operator whose first interrupt was
// not enough needs the terminal back; the run stays resumable either way.
func RunWithSignals(args []string, stdout, stderr io.Writer) int {
	return runWithSignals(args, stdout, stderr, Run, os.Exit)
}

// runWithSignals carries RunWithSignals' logic with both process-level effects
// injected — the command runner and the immediate exit — so the second-signal path
// can be tested without a test process ending itself (T052).
func runWithSignals(args []string, stdout, stderr io.Writer,
	run func(context.Context, []string, io.Writer, io.Writer) int, exit func(int)) int {
	// Two registrations on purpose: the context carries the first signal to the
	// work, this channel counts signals so a second one can end the wait.
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-interrupts
		<-interrupts
		fmt.Fprint(stderr, "zamnius: second signal received, exiting immediately; the run stays resumable\n")
		exit(ExitError)
	}()

	return run(ctx, args, stdout, stderr)
}
