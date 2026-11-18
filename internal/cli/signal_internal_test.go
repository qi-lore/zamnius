package cli

// This file covers T052 at the level where signals are deterministic: the signal
// binding is exercised with a scripted command and a captured exit, so the
// second-signal path — which ends a real process — can be asserted without ending
// the test process (contracts/cli.md §3). The end-to-end path through a real
// SIGTERM is covered by test/integration/signal_test.go.

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestFirstSignalCancelsTheWorkAndReturnsTheCancelledCode proves the graceful path:
// one SIGTERM ends the command's context, the command unwinds, and the process exit
// code is the cancelled code the contract calls exit `7`.
func TestFirstSignalCancelsTheWorkAndReturnsTheCancelledCode(t *testing.T) {
	entered := make(chan struct{})
	run := func(ctx context.Context, _ []string, _, _ io.Writer) int {
		close(entered)
		<-ctx.Done()
		return ExitCancelled
	}

	done := make(chan int, 1)
	go func() {
		done <- runWithSignals(nil, io.Discard, io.Discard, run, func(code int) {
			t.Errorf("exit(%d) called for a single signal; the command must unwind instead", code)
		})
	}()

	<-entered
	interruptSelf(t, syscall.SIGTERM)

	select {
	case code := <-done:
		if code != ExitCancelled {
			t.Errorf("exit code after one signal = %d, want %d", code, ExitCancelled)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("one signal did not cancel the command; the binding is not wired")
	}
}

// TestSecondSignalExitsImmediatelyWithoutWaitingForTheUnwind proves the escape
// hatch: while the command is still unwinding after the first signal, a second
// signal ends the process at once with exit `1` rather than waiting for the engine
// to finish committing.
func TestSecondSignalExitsImmediatelyWithoutWaitingForTheUnwind(t *testing.T) {
	entered := make(chan struct{})
	unwinding := make(chan struct{})
	release := make(chan struct{})

	run := func(ctx context.Context, _ []string, _, _ io.Writer) int {
		close(entered)
		<-ctx.Done()
		close(unwinding)
		<-release // a slow unwind is what gives the second signal something to cut short
		return ExitCancelled
	}

	exited := make(chan int, 1)
	done := make(chan int, 1)
	go func() {
		done <- runWithSignals(nil, io.Discard, io.Discard, run, func(code int) { exited <- code })
	}()

	<-entered
	// The second signal is sent only once the first has been delivered to the
	// command, so the kernel cannot coalesce the two into one pending SIGTERM.
	interruptSelf(t, syscall.SIGTERM)
	<-unwinding
	interruptSelf(t, syscall.SIGTERM)

	select {
	case code := <-exited:
		if code != ExitError {
			t.Errorf("exit code after a second signal = %d, want %d", code, ExitError)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a second signal did not end the process")
	}

	// Let the scripted command finish so the binding returns and stops handling
	// signals; a signal delivered after that would end the test process.
	close(release)
	if code := <-done; code != ExitCancelled {
		t.Errorf("the unwinding command returned %d, want %d", code, ExitCancelled)
	}
}

// interruptSelf delivers a signal to this process. The binding under test is
// registered before the helper is called, so the signal is handled rather than
// acting on its default disposition.
func interruptSelf(t *testing.T, sig syscall.Signal) {
	t.Helper()

	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("send %v to self: %v", sig, err)
	}
}
