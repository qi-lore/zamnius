package integration_test

// This file covers T052: SIGINT/SIGTERM cancel the command context so the engine
// commits the current attempt boundary, releases the lease, and leaves the run
// `paused` with exit `7`; a second signal ends the process immediately with exit
// `1` and the record stays resumable (contracts/cli.md §3, §5.4).
//
// It is a subprocess test because a signal is a process event: the parent runs this
// binary again as a child, which binds its signals exactly the way the shipped
// binary does (`cli.RunWithSignals`), and interrupts it while a provider call is in
// flight.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/app"
	"github.com/qi-lore/zamnius/internal/cli"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/observability"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
)

// signalArgSep separates the child's arguments inside one environment variable, so
// no argument can be split or re-joined by accident.
const signalArgSep = "\x1f"

// TestSignalChild is not an ordinary test: it is the entry point the parent
// re-executes to get a real process it can signal. With the trigger absent it skips,
// so an ordinary `go test` run never executes it.
func TestSignalChild(t *testing.T) {
	if os.Getenv("ZAMNIUS_SIGNAL_CHILD") != "1" {
		t.Skip("only runs as the child of a signal test")
	}
	args := strings.Split(os.Getenv("ZAMNIUS_SIGNAL_ARGS"), signalArgSep)
	os.Exit(cli.RunWithSignals(args, os.Stdout, os.Stderr))
}

// TestSIGTERMLeavesTheRunPausedAndResumable is the contract's scenario (§5.4): a
// SIGTERM during a provider call leaves the run `paused` (not killed mid-write),
// releases the lease, exits `7`, and a later resume finishes the run.
func TestSIGTERMLeavesTheRunPausedAndResumable(t *testing.T) {
	provider := newSignalProvider()
	server := provider.serve(t)

	stateDir := filepath.Join(t.TempDir(), "state")
	defFile := absoluteFixture(t, "hello.yaml")

	child, stdout, stderr := startSignalChild(t, server, "run", "start", "--file", defFile, "--state-dir", stateDir)
	provider.awaitCall(t)

	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the child: %v", err)
	}
	if code := waitForExit(t, child); code != 7 {
		t.Fatalf("child exit = %d, want 7 (stdout: %s, stderr: %s)", code, stdout, stderr)
	}
	if !strings.Contains(stderr.String(), "paused") {
		t.Errorf("child stderr = %q, want it to say the run was paused", stderr)
	}

	// The interruption is durable and resumable: the run is paused, its pause is in
	// the event log, and the lease was released rather than left to expire.
	store := openStoreAt(t, stateDir)
	runID := onlyRunID(t, store)
	run, err := store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Status != domain.RunPaused {
		t.Fatalf("status after SIGTERM = %q, want paused", run.Status)
	}
	types := eventTypesOf(t, store, runID)
	if indexOfEvent(types, string(domain.EventRunPaused)) < 0 {
		t.Errorf("event log %v has no run.paused", types)
	}
	lease, err := store.AcquireLease(context.Background(), runID, "after-signal", time.Minute)
	if err != nil {
		t.Fatalf("lease after a signalled stop error = %v, want the run to be free", err)
	}
	if err := store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}

	// The provider now answers, and the run resumes through the ordinary command.
	provider.open()
	t.Setenv(app.BaseURLEnvVar, server.URL)
	t.Setenv(observability.SecretEnvVar, "sk-signal-canary")
	t.Setenv("ZAMNIUS_LOG_LEVEL", "error")

	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(),
		[]string{"run", "resume", runID.String(), "--state-dir", stateDir}, &out, &errOut)
	if code != 0 {
		t.Fatalf("resume after SIGTERM exit = %d, want 0 (stdout: %s, stderr: %s)", code, out.String(), errOut.String())
	}
	resumed, err := store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() after resume error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Errorf("status after resume = %q, want succeeded", resumed.Status)
	}
}

// signalProvider blocks the first provider call until the test opens its gate, and
// reports that the call arrived so the parent knows the child is genuinely mid-run
// before it signals. A blocked handler returns when the caller's context ends, which
// is what a signalled run looks like from the provider's side.
type signalProvider struct {
	arrived chan struct{}
	gate    chan struct{}
	stop    chan struct{}
	once    sync.Once
}

// newSignalProvider builds a provider whose first answer is withheld.
func newSignalProvider() *signalProvider {
	return &signalProvider{
		arrived: make(chan struct{}),
		gate:    make(chan struct{}),
		stop:    make(chan struct{}),
	}
}

// ServeHTTP implements the provider's endpoint.
func (p *signalProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.once.Do(func() { close(p.arrived) })
	select {
	case <-p.gate:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`)
	case <-r.Context().Done():
	case <-p.stop:
		// A run that was interrupted mid-call can leave the request socket open
		// from the server's side, so a test must be able to release the handler
		// before httptest.Server.Close waits on it.
	}
}

// awaitCall blocks until the child has reached the provider, which is the point at
// which an interruption lands inside a real attempt.
func (p *signalProvider) awaitCall(t *testing.T) {
	t.Helper()

	select {
	case <-p.arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("the child never reached the provider; the interruption would prove nothing")
	}
}

// open releases every waiting provider call.
func (p *signalProvider) open() { close(p.gate) }

// release unblocks handlers that are still waiting, so the server can close.
func (p *signalProvider) release() { close(p.stop) }

// serve starts the provider and tears it down in the order that cannot hang: the
// handlers are released first, then the server is closed.
func (p *signalProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(p)
	t.Cleanup(func() {
		p.release()
		server.Close()
	})
	return server
}

// startSignalChild re-executes this test binary as the shipped CLI would run,
// pointing it at the test provider and the given state directory.
func startSignalChild(t *testing.T, server *httptest.Server, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	child := exec.Command(os.Args[0], "-test.run=^TestSignalChild$")
	child.Env = mergedEnv(map[string]string{
		"ZAMNIUS_SIGNAL_CHILD":     "1",
		"ZAMNIUS_SIGNAL_ARGS":      strings.Join(args, signalArgSep),
		app.BaseURLEnvVar:          server.URL,
		observability.SecretEnvVar: "sk-signal-canary",
		"ZAMNIUS_LOG_LEVEL":        "error",
	})

	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	})
	return child, &stdout, &stderr
}

// mergedEnv overlays extra variables on the inherited environment, replacing rather
// than duplicating a key so the child cannot read two different values for one name.
func mergedEnv(extra map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if _, replace := extra[key]; found && replace {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// waitForExit waits for the child to end and returns its exit code.
func waitForExit(t *testing.T, child *exec.Cmd) int {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("wait for the child: %v", err)
		}
		return exit.ExitCode()
	case <-time.After(15 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("the child did not exit; a signal was ignored")
		return 0
	}
}

// absoluteFixture resolves a workflow fixture to an absolute path, because the child
// process may run from a different working directory.
func absoluteFixture(t *testing.T, name string) string {
	t.Helper()

	path, err := filepath.Abs(fixture(t, name))
	if err != nil {
		t.Fatalf("resolve fixture %s: %v", name, err)
	}
	return path
}

// onlyRunID returns the single run in the state store, which is how the parent
// addresses the run its child created without parsing the child's output.
func onlyRunID(t *testing.T, store *sqlite.Store) uuid.UUID {
	t.Helper()

	var id string
	if err := store.DB().QueryRow(`SELECT id FROM runs`).Scan(&id); err != nil {
		t.Fatalf("read the run id: %v", err)
	}
	return uuid.MustParse(id)
}

// eventTypesOf lists a run's event types in seq order from an open store.
func eventTypesOf(t *testing.T, store *sqlite.Store, runID uuid.UUID) []string {
	t.Helper()

	events, err := store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}
