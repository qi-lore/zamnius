package recoverability_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/internal/store/sqlite"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// The genuine-process-death harness (US2 scenario 1, SC-001, SC-002). A fault point
// panics inside the process, which still lets deferred cleanup run; a real crash
// does not. This test therefore re-executes the test binary as a child and kills it
// with SIGKILL — no deferred function runs, no lease is released, no buffer is
// flushed — and then resumes from a fresh instance that holds only the store.
//
// The child and the parent speak over the child's stdout, which is the only channel
// a dying process can be observed through:
//
//	CHILD-RUN-ID <uuid>       the run's identifier, so the parent knows what to resume
//	CHILD-BRANCH-BLOCKED      the branch that must be interrupted has started
//	CHILD-BRANCH-COMMITTED    a sibling branch is durably committed, so the kill lands
//	                          after committed work exists that the resume must reuse
const (
	childModeEnv  = "ZAMNIUS_SIGKILL_CHILD"
	childStateEnv = "ZAMNIUS_SIGKILL_STATE"

	runIDMarker     = "CHILD-RUN-ID "
	blockedMarker   = "CHILD-BRANCH-BLOCKED"
	committedMarker = "CHILD-BRANCH-COMMITTED"

	// handshakeTimeout bounds how long the parent waits for the child to reach the
	// intended state. Generous, because a cold test binary must start first.
	handshakeTimeout = 60 * time.Second

	// childBlockWindow is how long the interrupted branch waits before giving up.
	// It exists only so a leaked child cannot outlive the test run: the parent kills
	// this process long before the window closes, and the armed timer keeps the Go
	// runtime's deadlock detector quiet while every goroutine waits.
	childBlockWindow = 10 * time.Minute

	// commitPollInterval is how often the child checks its own store for the
	// completed sibling branch.
	commitPollInterval = 2 * time.Millisecond
)

// TestMain lets this package's binary run in two modes: normally it runs the tests,
// and with childModeEnv set it becomes the process that dies by signal. Re-execution
// is the only way to test death by SIGKILL, which cannot be simulated in-process.
func TestMain(m *testing.M) {
	if os.Getenv(childModeEnv) == "1" {
		os.Exit(runChild())
	}
	os.Exit(m.Run())
}

// TestResumeAfterProcessKill interrupts a live run with SIGKILL in the middle of a
// fan-out and proves a fresh instance recovers it completely: the committed sibling
// branch is reused rather than re-executed, the branch that died mid-flight is
// restarted exactly once, the abandoned attempt and its restart cause are on record,
// and the resulting artifacts are byte-identical to an uninterrupted control run.
func TestResumeAfterProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL does not exist on Windows; this suite targets Linux and macOS")
	}

	base := t.TempDir()
	controlDir := filepath.Join(base, "control")
	runDir := filepath.Join(base, "run")

	def, err := branchingDefinition()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}

	// The control run: the same workflow, uninterrupted, so the resumed artifacts
	// can be compared byte for byte rather than merely counted (SC-003).
	controlFixture, controlRun := runControl(t, controlDir, &answerer{}, def)
	controlDigests := digestsOf(t, controlFixture, controlRun)

	// The child runs until a branch is committed and its sibling is mid-flight, and
	// is then killed outright.
	runID := runChildToDeath(t, runDir)

	// A SIGKILL releases nothing: the dead owner's lease is still held, so a real
	// recovery waits for it to lapse before taking over (ADR-0004).
	time.Sleep(2 * leaseTTL)

	second := &answerer{}
	resumed := openInstance(t, runDir, "owner-sigkill-parent", second, fakes.NewIDSource())

	// The run the child left behind is mid-flight, not terminal: the kill landed
	// where this test needs it to.
	before, err := resumed.store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if before.Status != domain.RunRunning {
		t.Fatalf("run status after SIGKILL = %q, want %q: the child did not die mid-run", before.Status, domain.RunRunning)
	}

	result, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded (err = %v)", result.Status, result.Err)
	}

	// Completed work survives the crash and is never re-executed (FR-013, SC-002).
	if got := second.callsFor("outline"); got != 0 {
		t.Errorf("resume re-executed the branch the child had committed %d times, want 0", got)
	}
	// The branch that died mid-flight restarts exactly once (US2 scenario 7).
	if got := second.callsFor("details"); got != 1 {
		t.Errorf("resume executed the interrupted branch %d times, want exactly 1", got)
	}

	// Artifacts are byte-identical to the control run: the same documents, addressed
	// by the same digests (SC-003).
	resumedDigests := digestsOf(t, resumed, runID)
	if len(resumedDigests) != len(controlDigests) {
		t.Errorf("resumed artifacts = %d, want the control's %d", len(resumedDigests), len(controlDigests))
	}
	for digest := range controlDigests {
		if !resumedDigests[digest] {
			t.Errorf("artifact %s from the control run is missing after resume", digest)
		}
	}

	events := eventsOf(t, resumed, runID)

	// The attempt the SIGKILL interrupted is named on the record, so a reader with no
	// other context can tell which attempt was abandoned (US3 scenario 1).
	abandoned := eventsOfType(events, string(domain.EventAttemptAbandoned))
	if len(abandoned) != 1 {
		t.Errorf("attempt.abandoned events = %d, want exactly 1", len(abandoned))
	}
	if len(abandoned) == 1 && abandoned[0].AttemptID == nil {
		t.Error("attempt.abandoned does not name the interrupted attempt")
	}

	// The restart carries its cause, so the recovery itself is auditable.
	decisions, err := resumed.store.Decisions(context.Background(), runID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	restarts := 0
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionResumeRestart {
			restarts++
			if decision.Rationale == "" {
				t.Error("resume_restart decision has no rationale naming the cause")
			}
		}
	}
	if restarts == 0 {
		t.Error("no resume_restart decision recorded for an attempt abandoned by a process death")
	}

	// The interrupted branch produced a second attempt, so both are on record.
	if attempts := attemptsOf(t, resumed, runID, "draft"); len(attempts) < 2 {
		t.Errorf("attempts after resume = %d, want the committed one plus the restarted one", len(attempts))
	}

	// No side effect is duplicated by the restart (US2 scenario 3).
	assertNoDuplicateCommitPerAttempt(t, events)
}

// runChildToDeath starts the test binary as the child, waits until it has reached the
// intended state, kills it with SIGKILL, and returns the run id it created. It is a
// t.Helper so a failure points at the calling test.
func runChildToDeath(t *testing.T, dir string) uuid.UUID {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), childModeEnv+"=1", childStateEnv+"="+dir)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("child stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	// A child that never gets killed must not outlive the test.
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	lines := scanLines(stdout)

	// Wait for all three signals in any order: the run id can only be reported after
	// the engine starts, and "blocked" and "committed" race each other because the two
	// branches run concurrently.
	var (
		runID     uuid.UUID
		needID    = true
		needBlock = true
		needDone  = true
	)
	deadline := time.Now().Add(handshakeTimeout)
	for needID || needBlock || needDone {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("child did not reach a committed branch with a sibling in flight within %s", handshakeTimeout)
		}
		line, ok := nextLine(lines, remaining)
		if !ok {
			t.Fatal("child exited before reporting the run id, a blocked branch, and a committed branch")
		}
		switch {
		case strings.HasPrefix(line, runIDMarker):
			parsed, parseErr := uuid.Parse(strings.TrimSpace(strings.TrimPrefix(line, runIDMarker)))
			if parseErr != nil {
				t.Fatalf("child reported an unparsable run id in %q: %v", line, parseErr)
			}
			runID = parsed
			needID = false
		case line == blockedMarker:
			needBlock = false
		case line == committedMarker:
			needDone = false
		}
	}

	// The interruption under test: SIGKILL, which no handler can intercept.
	if err := cmd.Process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			t.Fatal("child exited before the kill; the interruption did not land where this test needs it")
		}
		t.Fatalf("kill child process: %v", err)
	}
	// A signal death reports an *exec.ExitError, so a nil error here would mean the
	// child exited by itself and the test proved nothing.
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited cleanly instead of dying by signal")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child ended with %v, want death by SIGKILL", cmd.ProcessState)
	}

	return runID
}

// scanLines turns the child's stdout into a stream of lines, so the parent can react
// to a signal without knowing how much output surrounds it.
func scanLines(r io.Reader) <-chan string {
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

// nextLine returns the next line the child wrote, or false when the stream closed or
// the wait lapsed.
func nextLine(lines <-chan string, wait time.Duration) (string, bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case line, ok := <-lines:
		return line, ok
	case <-timer.C:
		return "", false
	}
}

// runChild executes the child half of the harness. It starts a real run in the state
// directory the parent prepared, reports its progress on stdout, and blocks inside a
// branch. Returning at all means the parent failed to kill it in time, which is a
// harness failure rather than a product result.
func runChild() int {
	dir := os.Getenv(childStateEnv)
	if dir == "" {
		fmt.Fprintf(os.Stderr, "child: %s is not set\n", childStateEnv)
		return 1
	}

	def, err := branchingDefinition()
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: build definition: %v\n", err)
		return 1
	}

	// The child wires the engine exactly as the in-process tests do, so the process
	// boundary is the only difference between this run and an uninterrupted one.
	instance, err := newInstance(dir, "owner-sigkill-child", blockableModel{}, fakes.NewIDSource())
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: %v\n", err)
		return 1
	}

	observer := &runIDObserver{reported: make(chan uuid.UUID, 1)}
	started := make(chan struct{})
	go func() {
		defer close(started)
		if _, startErr := instance.engine.Start(context.Background(), def, engine.StartOptions{Observer: observer}); startErr != nil {
			fmt.Fprintf(os.Stderr, "child: Start() error = %v\n", startErr)
		}
	}()

	// Wait for the engine to name the run, then watch the store for the sibling
	// branch committing. Polling the durable record — rather than trusting timing —
	// is what makes "a branch was committed before the kill" a fact, not a hope.
	select {
	case runID := <-observer.reported:
		go reportCommittedBranch(instance.store, runID)
	case <-time.After(handshakeTimeout):
		fmt.Fprintln(os.Stderr, "child: the engine never started a phase")
		return 1
	}

	<-started
	fmt.Fprintln(os.Stderr, "child: Start() returned; the parent did not kill this process in time")
	return 1
}

// blockableModel answers the branch that must be committed before the kill, and
// blocks the branch that must be interrupted. It ignores context cancellation on
// purpose: only a process-level signal can end it, which is the property under test.
type blockableModel struct{}

// Complete implements the engine's model port.
func (blockableModel) Complete(_ context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	if req.Metadata["attempt"] == "details" {
		// Announce the blocked branch before waiting, so the parent knows the
		// interruption will land inside an attempt rather than between them.
		fmt.Println(blockedMarker)
		select {
		case <-time.After(childBlockWindow):
		}
	}
	return domain.ModelResponse{
		Content:    "document for " + req.Metadata["attempt"],
		Usage:      domain.Usage{InputTokens: 5, OutputTokens: 3},
		StopReason: domain.StopEnd,
	}, nil
}

// runIDObserver reports the run's identifier as soon as the engine starts its first
// phase, which is the earliest point the run exists durably.
type runIDObserver struct {
	once     sync.Once
	reported chan uuid.UUID
}

// OnPhaseStarted implements the engine's observer port.
func (o *runIDObserver) OnPhaseStarted(_ context.Context, runID uuid.UUID, _ string) {
	o.once.Do(func() {
		fmt.Println(runIDMarker + runID.String())
		o.reported <- runID
	})
}

// OnAttemptFinished implements the engine's observer port.
func (o *runIDObserver) OnAttemptFinished(context.Context, uuid.UUID, string, int, domain.ErrorClass) {
}

// OnPhaseFinished implements the engine's observer port.
func (o *runIDObserver) OnPhaseFinished(context.Context, uuid.UUID, string, domain.PhaseStatus) {
}

// reportCommittedBranch watches the durable record until a branch of the fan-out
// phase has succeeded, then tells the parent that committed work now exists.
func reportCommittedBranch(store *sqlite.Store, runID uuid.UUID) {
	deadline := time.Now().Add(handshakeTimeout)
	for time.Now().Before(deadline) {
		attempts, err := store.Attempts(context.Background(), runID, "draft")
		if err == nil {
			for _, attempt := range attempts {
				if attempt.Status == domain.AttemptSucceeded {
					fmt.Println(committedMarker)
					return
				}
			}
		}
		time.Sleep(commitPollInterval)
	}
	fmt.Fprintln(os.Stderr, "child: no branch committed within the deadline")
}
