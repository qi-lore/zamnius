package recoverability_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/engine"
	"github.com/hIMEI29A/zamnius/internal/fault"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

// startExpectingFault runs a definition and asserts that an armed fault point
// interrupted it. A hit panics rather than returning an error by design, so the
// interruption is recovered exactly where a real crash would end the process.
func startExpectingFault(t *testing.T, f *fixture, def definition.Definition) {
	t.Helper()

	interrupted := false
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if _, ok := fault.AsTriggered(recovered); !ok {
				t.Fatalf("run panicked with an unexpected value: %v", recovered)
			}
			interrupted = true
		}()
		_, _ = f.engine.Start(context.Background(), def, engine.StartOptions{})
	}()

	if !interrupted {
		t.Fatal("the armed fault point never triggered; the test proved nothing")
	}
	// The resume must not inherit the interruption, so the point is disarmed as
	// soon as the crash is captured.
	fault.Disarm()
}

// TestResumeAfterFaultAtEveryInterruptionPoint is the core recoverability proof
// (SC-001, SC-002, SC-003, US2 scenarios 1 and 3). For each point where a crash can
// land — before and after the artifact rename, and before and after the committing
// transaction — a fresh instance resumes the run and must produce the same
// artifacts as an uninterrupted control run, without re-executing completed work.
func TestResumeAfterFaultAtEveryInterruptionPoint(t *testing.T) {
	cases := []struct {
		name  string
		point string
		// abandoned is true when the interrupted attempt never committed an outcome,
		// which is the case everywhere except a crash *after* the commit.
		abandoned bool
		// reuse is true when the interrupted attempt committed a success, so the
		// resume must reuse it rather than call the model again.
		reuse bool
	}{
		{name: "before_artifact_rename", point: fault.BeforeArtifactRename, abandoned: true},
		{name: "after_artifact_rename", point: fault.AfterArtifactRename, abandoned: true},
		{name: "before_commit", point: fault.BeforeCommit, abandoned: true},
		{name: "after_commit", point: fault.AfterCommit, reuse: true},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			// The fault registry is process-global, so these subtests must not run
			// concurrently with each other: one subtest's disarm would erase another's
			// armed point and the crash under test would never land.
			base := t.TempDir()
			controlDir := filepath.Join(base, "control")
			runDir := filepath.Join(base, "run")

			def := twoPhaseDefinition(t)

			// The control run: same workflow, no interruption.
			controlFixture, controlRun := runControl(t, controlDir, &answerer{}, def)
			controlDigests := digestsOf(t, controlFixture, controlRun)

			// The interrupted run. The fault is armed from inside the model call for
			// the second phase, so the first phase commits normally and the crash
			// lands squarely in the phase under test.
			interrupted := &answerer{onCall: func(req domain.ModelRequest) {
				if req.Metadata["phase"] == "review" {
					fault.Arm(testCase.point)
				}
			}}
			t.Cleanup(fault.Disarm)

			ids, runIDOf := predictRunID()
			first := openInstance(t, runDir, "owner-first", interrupted, ids)
			startExpectingFault(t, first, def)
			runID := runIDOf()

			if got := interrupted.callsFor("draft"); got != 1 {
				t.Fatalf("draft was called %d times before the interruption, want 1", got)
			}

			// A fresh instance, holding only the store. The crashed process still
			// holds the lease, so the first owner's claim must lapse before the
			// second can take over.
			time.Sleep(2 * leaseTTL)
			second := &answerer{}
			resumed := openInstance(t, runDir, "owner-second", second, fakes.NewIDSource())

			result, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
			if err != nil {
				t.Fatalf("Resume() error = %v", err)
			}
			if result.Status != domain.RunSucceeded {
				t.Fatalf("resumed status = %q, want succeeded", result.Status)
			}

			// Completed work is never re-executed: the first phase ran once in the
			// crashed process and not at all in the resumed one (FR-013, SC-002).
			if got := second.callsFor("draft"); got != 0 {
				t.Errorf("resume re-executed the completed first phase %d times, want 0", got)
			}
			if testCase.reuse {
				if got := second.callsFor("review"); got != 0 {
					t.Errorf("resume called the committed review attempt %d times, want 0", got)
				}
			} else if got := second.callsFor("review"); got != 1 {
				t.Errorf("resume called the restarted review attempt %d times, want 1", got)
			}

			// Artifacts are byte-identical to the control run: the same documents,
			// addressed by the same digests (SC-003).
			resumedDigests := digestsOf(t, resumed, runID)
			if len(resumedDigests) != len(controlDigests) {
				t.Fatalf("resumed artifacts = %d, want the control's %d", len(resumedDigests), len(controlDigests))
			}
			for digest := range controlDigests {
				if !resumedDigests[digest] {
					t.Errorf("artifact %s from the control run is missing after resume", digest)
				}
			}

			events := eventsOf(t, resumed, runID)
			abandoned := eventsOfType(events, string(domain.EventAttemptAbandoned))
			if testCase.abandoned {
				if len(abandoned) != 1 {
					t.Errorf("attempt.abandoned events = %d, want 1", len(abandoned))
				}
				if len(abandoned) == 1 && abandoned[0].AttemptID == nil {
					t.Error("attempt.abandoned does not name the interrupted attempt")
				}
			} else if len(abandoned) != 0 {
				t.Errorf("attempt.abandoned events = %d, want 0 when the outcome was committed", len(abandoned))
			}

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
			if testCase.abandoned && restarts == 0 {
				t.Error("no resume_restart decision recorded for an abandoned attempt")
			}
			if testCase.reuse && restarts != 0 {
				t.Errorf("resume_restart decisions = %d, want 0 when nothing needed restarting", restarts)
			}

			// No side effect is duplicated: no attempt lists the same digest twice
			// (US2 scenario 3).
			assertNoDuplicateCommitPerAttempt(t, events)
		})
	}
}

// TestResumeReusesCompletedBranches covers US2 scenario 1 with a fan-out: a branch
// that finished before the interruption is reused, the unfinished branch runs, and
// the join completes with the full set of results.
func TestResumeReusesCompletedBranches(t *testing.T) {
	base := t.TempDir()
	controlDir := filepath.Join(base, "control")
	runDir := filepath.Join(base, "run")

	def := fanoutDefinition(t)

	controlFixture, controlRun := runControl(t, controlDir, &answerer{}, def)
	controlDigests := digestsOf(t, controlFixture, controlRun)

	// "outline" answers immediately; "details" blocks until released, so cancelling
	// mid-fan-out leaves outline committed and details unfinished.
	gate := make(chan struct{})
	model := &gatedAnswerer{gate: gate}
	t.Cleanup(model.release)

	ids, runIDOf := predictRunID()
	first := openInstance(t, runDir, "owner-first", model, ids)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan engine.RunResult, 1)
	go func() {
		result, _ := first.engine.Start(ctx, def, engine.StartOptions{})
		done <- result
	}()

	// The run id is the first identifier Start draws; wait until it has been drawn.
	runID := waitForRunID(t, runIDOf)

	// Wait until the fast branch is durably committed, which is the state the
	// resume has to reuse. Polling the store is the only honest way to observe it:
	// the branch runs in its own goroutine.
	waitForSucceededAttempt(t, first, runID, "draft")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after cancellation")
	}

	outlineCallsBefore := model.callsFor("outline")
	if outlineCallsBefore != 1 {
		t.Fatalf("the completed branch ran %d times before the interruption, want 1", outlineCallsBefore)
	}

	// The completed branch is on record before the resume.
	beforeResume := attemptsOf(t, first, runID, "draft")
	succeeded := 0
	for _, attempt := range beforeResume {
		if attempt.Status == domain.AttemptSucceeded {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded attempts before resume = %d, want 1 (the branch that answered)", succeeded)
	}

	// Release the blocked branch and resume from a fresh instance.
	model.release()
	second := &answerer{}
	resumed := openInstance(t, runDir, "owner-second", second, fakes.NewIDSource())

	result, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", result.Status)
	}

	// The completed branch was not executed again (FR-013, SC-002).
	if got := second.callsFor("outline"); got != 0 {
		t.Errorf("resume executed the completed branch %d times, want 0", got)
	}
	if got := model.callsFor("outline"); got != outlineCallsBefore {
		t.Errorf("the completed branch ran %d times in total, want the %d before the interruption", got, outlineCallsBefore)
	}
	// The unfinished branch did run.
	if got := second.callsFor("details"); got != 1 {
		t.Errorf("resume executed the unfinished branch %d times, want 1", got)
	}

	// The join completed with the full set of results, byte-identical to control.
	resumedDigests := digestsOf(t, resumed, runID)
	for digest := range controlDigests {
		if !resumedDigests[digest] {
			t.Errorf("artifact %s from the control run is missing after resume", digest)
		}
	}
	attempts := attemptsOf(t, resumed, runID, "draft")
	if len(attempts) < 2 {
		t.Fatalf("attempts after resume = %d, want the reused one plus the restarted one", len(attempts))
	}
}

// waitForRunID blocks until Start has drawn the run's identifier.
func waitForRunID(t *testing.T, runIDOf func() uuid.UUID) uuid.UUID {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if id := runIDOf(); id != uuid.Nil {
			return id
		}
		if time.Now().After(deadline) {
			t.Fatal("Start did not assign a run id within the deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForSucceededAttempt polls the store until the phase has a succeeded attempt,
// which is the durable marker that a branch finished.
func waitForSucceededAttempt(t *testing.T, f *fixture, runID uuid.UUID, phase string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		attempts, err := f.store.Attempts(context.Background(), runID, phase)
		if err != nil {
			t.Fatalf("Attempts(%s) error = %v", phase, err)
		}
		for _, attempt := range attempts {
			if attempt.Status == domain.AttemptSucceeded {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no attempt of phase %q succeeded within the deadline", phase)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// gatedAnswerer is a deterministic model in which one branch blocks until released.
// It is what makes one branch finish while its sibling is still in flight.
type gatedAnswerer struct {
	gate chan struct{}
	once sync.Once

	mu    sync.Mutex
	calls []domain.ModelRequest
}

func (g *gatedAnswerer) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	g.mu.Lock()
	g.calls = append(g.calls, req)
	g.mu.Unlock()

	if req.Metadata["attempt"] == "details" {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return domain.ModelResponse{}, &domain.Error{
				Class: domain.ErrClassCancelled,
				Msg:   "branch cancelled while waiting",
				Err:   ctx.Err(),
			}
		}
	}
	return domain.ModelResponse{
		Content:    "document for " + req.Metadata["attempt"],
		Usage:      domain.Usage{InputTokens: 5, OutputTokens: 3},
		StopReason: domain.StopEnd,
	}, nil
}

func (g *gatedAnswerer) callsFor(label string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	count := 0
	for _, call := range g.calls {
		if call.Metadata["attempt"] == label {
			count++
		}
	}
	return count
}

func (g *gatedAnswerer) release() { g.once.Do(func() { close(g.gate) }) }

// eventsOf reads the full event log for a run.
func eventsOf(t *testing.T, f *fixture, runID uuid.UUID) []domain.Event {
	t.Helper()

	events, err := f.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	return events
}

// eventsOfType filters the log by event type.
func eventsOfType(events []domain.Event, want string) []domain.Event {
	out := make([]domain.Event, 0)
	for _, event := range events {
		if event.Type == want {
			out = append(out, event)
		}
	}
	return out
}

// assertNoDuplicateCommitPerAttempt proves no side effect was recorded twice for
// one attempt: an artifact.committed event may appear once per digest per attempt,
// never twice (US2 scenario 3).
func assertNoDuplicateCommitPerAttempt(t *testing.T, events []domain.Event) {
	t.Helper()

	seen := make(map[string]map[string]bool)
	for _, event := range events {
		if event.Type != string(domain.EventArtifactCommitted) || event.AttemptID == nil {
			continue
		}
		var payload struct {
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("event %d payload is not JSON: %v", event.Seq, err)
		}
		key := event.AttemptID.String()
		if seen[key] == nil {
			seen[key] = make(map[string]bool)
		}
		if seen[key][payload.SHA256] {
			t.Errorf("attempt %s committed artifact %s twice", key, payload.SHA256)
		}
		seen[key][payload.SHA256] = true
	}
}
