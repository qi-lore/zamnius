package recoverability_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/engine"
	"github.com/hIMEI29A/zamnius/internal/fault"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

// This file covers attempt reconciliation (T047, FR-063, US2 scenario 7): an
// attempt that has no committed outcome is restarted with a fresh attempt number
// that names the attempt it replaced, and the increment's one side effect — the
// content-addressed artifact write — is identified by its content key so a restart
// cannot record it twice.

// TestResumeRecordsRestartedFrom covers the crash case: an attempt was running when
// the process died, so it has no committed outcome. Resume marks it abandoned and
// the restarted attempt points back at it through restarted_from, which is how a
// reader with no other context can tell the two rows are the same step
// (data-model.md §4 invariant 2).
func TestResumeRecordsRestartedFrom(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "run")
	def := twoPhaseDefinition(t)

	// Crash the second phase mid-attempt, before its outcome is committed.
	interrupted := &answerer{onCall: func(req domain.ModelRequest) {
		if req.Metadata["phase"] == "review" {
			fault.Arm(fault.BeforeCommit)
		}
	}}
	t.Cleanup(fault.Disarm)

	ids, runIDOf := predictRunID()
	first := openInstance(t, runDir, "owner-first", interrupted, ids)
	startExpectingFault(t, first, def)
	runID := runIDOf()

	time.Sleep(2 * leaseTTL)
	resumed := openInstance(t, runDir, "owner-second", &answerer{}, fakes.NewIDSource())
	if _, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	review := attemptsOf(t, resumed, runID, "review")
	var abandoned, restarted *domain.Attempt
	for i := range review {
		switch review[i].Status {
		case domain.AttemptAbandoned:
			abandoned = &review[i]
		case domain.AttemptSucceeded:
			restarted = &review[i]
		}
	}
	if abandoned == nil {
		t.Fatal("no abandoned review attempt was recorded")
	}
	if restarted == nil {
		t.Fatal("no succeeded review attempt was recorded")
	}
	if restarted.RestartedFrom == nil {
		t.Fatal("the restarted attempt does not name the attempt it replaced (restarted_from is nil)")
	}
	if *restarted.RestartedFrom != abandoned.ID {
		t.Errorf("restarted_from = %s, want the abandoned attempt %s", *restarted.RestartedFrom, abandoned.ID)
	}
	if restarted.AttemptNo <= abandoned.AttemptNo {
		t.Errorf("restarted attempt_no = %d, want a fresh number after %d", restarted.AttemptNo, abandoned.AttemptNo)
	}

	// The first phase ran once and was not a restart, so it must not claim one.
	for _, attempt := range attemptsOf(t, resumed, runID, "draft") {
		if attempt.RestartedFrom != nil {
			t.Errorf("draft attempt %d records restarted_from %s, want nil", attempt.AttemptNo, *attempt.RestartedFrom)
		}
	}
}

// TestResumeAfterPauseRecordsRestartedFrom covers the pause case: the owner was
// cancelled cleanly, so the attempt was already marked abandoned before the resume.
// The restart must still name it, or a paused-then-resumed step would lose the link
// between the attempt that was interrupted and the one that replaced it.
func TestResumeAfterPauseRecordsRestartedFrom(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "run")
	def := fanoutDefinition(t)

	// "outline" answers; "details" blocks until released, so cancelling leaves its
	// attempt abandoned while the sibling branch is already committed.
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

	runID := waitForRunID(t, runIDOf)
	waitForSucceededAttempt(t, first, runID, "draft")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after cancellation")
	}

	prior := latestAttemptFor(t, first, runID, "draft", "details")

	model.release()
	resumed := openInstance(t, runDir, "owner-second", &answerer{}, fakes.NewIDSource())
	if _, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	var restarted *domain.Attempt
	for _, attempt := range attemptsOf(t, resumed, runID, "draft") {
		if attempt.AgentName == "details" && attempt.Status == domain.AttemptSucceeded {
			restarted = &attempt
		}
	}
	if restarted == nil {
		t.Fatal("the interrupted branch was not restarted")
	}
	if restarted.RestartedFrom == nil {
		t.Fatal("the restarted branch does not name the attempt it replaced (restarted_from is nil)")
	}
	if *restarted.RestartedFrom != prior.ID {
		t.Errorf("restarted_from = %s, want the abandoned attempt %s", *restarted.RestartedFrom, prior.ID)
	}
}

// TestRestartDoesNotDuplicateSideEffectByContentKey is the idempotency proof for
// the increment's one side-effecting action, the artifact write. The interrupted
// attempt wrote its document to the store but crashed before committing the row;
// the restart reproduces the same bytes, so the side effect is identified by its
// content key and recorded once — the restart owns the single row and the abandoned
// attempt owns none (FR-063, FR-014, SC-003).
func TestRestartDoesNotDuplicateSideEffectByContentKey(t *testing.T) {
	base := t.TempDir()
	controlDir := filepath.Join(base, "control")
	runDir := filepath.Join(base, "run")
	def := twoPhaseDefinition(t)

	control, controlRun := runControl(t, controlDir, &answerer{}, def)
	controlDigests := digestsOf(t, control, controlRun)

	interrupted := &answerer{onCall: func(req domain.ModelRequest) {
		if req.Metadata["phase"] == "review" {
			fault.Arm(fault.BeforeCommit)
		}
	}}
	t.Cleanup(fault.Disarm)

	ids, runIDOf := predictRunID()
	first := openInstance(t, runDir, "owner-first", interrupted, ids)
	startExpectingFault(t, first, def)
	runID := runIDOf()

	time.Sleep(2 * leaseTTL)
	resumed := openInstance(t, runDir, "owner-second", &answerer{}, fakes.NewIDSource())
	if _, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	refs, err := resumed.store.Artifacts(context.Background(), runID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}

	countByDigest := make(map[string]int, len(refs))
	for _, ref := range refs {
		countByDigest[ref.SHA256]++
	}
	for digest := range controlDigests {
		if countByDigest[digest] != 1 {
			t.Errorf("artifact %s is recorded %d times, want exactly 1: a restart must not duplicate a recorded side effect",
				digest, countByDigest[digest])
		}
	}
	if len(countByDigest) != len(controlDigests) {
		t.Errorf("distinct artifacts = %d, want the control run's %d", len(countByDigest), len(controlDigests))
	}

	// The side effect belongs to the attempt that recorded it: the restart owns the
	// single review artifact, and the abandoned attempt owns none.
	var abandonedID, restartedID uuid.UUID
	for _, attempt := range attemptsOf(t, resumed, runID, "review") {
		switch attempt.Status {
		case domain.AttemptAbandoned:
			abandonedID = attempt.ID
		case domain.AttemptSucceeded:
			restartedID = attempt.ID
		}
	}
	if restartedID == uuid.Nil {
		t.Fatal("the review phase has no succeeded attempt after the restart")
	}
	for _, ref := range refs {
		if ref.Phase != "review" || ref.AttemptID == nil {
			continue
		}
		if *ref.AttemptID == abandonedID {
			t.Errorf("review artifact %s is owned by the abandoned attempt; the restart must own the recorded side effect", ref.SHA256)
		}
		if *ref.AttemptID != restartedID {
			t.Errorf("review artifact %s is owned by %s, want the restarted attempt %s", ref.SHA256, *ref.AttemptID, restartedID)
		}
	}
}

// latestAttemptFor returns the highest-numbered attempt of one agent in a phase,
// which is the attempt a resume would restart.
func latestAttemptFor(t *testing.T, f *fixture, runID uuid.UUID, phase, agent string) domain.Attempt {
	t.Helper()

	var latest domain.Attempt
	found := false
	for _, attempt := range attemptsOf(t, f, runID, phase) {
		if attempt.AgentName != agent {
			continue
		}
		if !found || attempt.AttemptNo > latest.AttemptNo {
			latest = attempt
			found = true
		}
	}
	if !found {
		t.Fatalf("no attempt of agent %q in phase %q", agent, phase)
	}
	return latest
}
