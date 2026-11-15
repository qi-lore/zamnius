package integration_test

// This file covers T053: the pause/resume persistence path. A pause must commit
// `run.paused` together with the status change and a `current_phase` that names the
// phase a resume will run next, so a reader holding only the store reports the
// paused position and the next expected action without replaying the log (FR-008,
// FR-023, data-model.md §2). The mirror case — a pause mid-phase keeps the phase it
// interrupted — is pinned where the interruption is produced, in cancel_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// cancelAfterPhase is a recording observer that cancels the run's context once a
// named phase reaches a terminal state. It makes a between-phase interruption
// deterministic: the cancel happens before the loop advances to the next phase, so
// the test observes exactly the state the pause must persist instead of racing the
// scheduler.
type cancelAfterPhase struct {
	*fakes.Observer
	phase  string
	cancel context.CancelFunc
}

// OnPhaseFinished records the fact and trips the cancellation, so the run is left
// paused with the interrupted phase already terminal.
func (o *cancelAfterPhase) OnPhaseFinished(ctx context.Context, runID uuid.UUID, phase string, status domain.PhaseStatus) {
	o.Observer.OnPhaseFinished(ctx, runID, phase, status)
	if phase == o.phase {
		o.cancel()
	}
}

// TestPauseRecordsTheResumePosition proves T053: a run interrupted between two
// phases commits `run.paused` with the status change and a `current_phase` naming
// the phase a resume will run next. The position is read back from the run row, so
// Status reports the paused position and the next expected action — and it is not
// the phase that already finished.
func TestPauseRecordsTheResumePosition(t *testing.T) {
	h := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		return ok("done", 5, 3), nil
	})

	def := h.workflow("two-phases", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first")).
			WithPhase("second", agent("second"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := &cancelAfterPhase{Observer: fakes.NewObserver(), phase: "first", cancel: cancel}

	result, err := h.engine.Start(ctx, def, engine.StartOptions{Observer: observer})
	if err == nil {
		t.Fatal("Start() returned no error for an interrupted run")
	}
	if result.Status != domain.RunPaused {
		t.Fatalf("run status = %q, want paused", result.Status)
	}

	// The position the pause persists is the next phase, not the finished one.
	status, err := h.engine.Status(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Status != domain.RunPaused {
		t.Errorf("stored status = %q, want paused", status.Status)
	}
	if status.CurrentPhase == nil || *status.CurrentPhase != "second" {
		t.Errorf("paused current_phase = %v, want \"second\"", status.CurrentPhase)
	}
	if !strings.Contains(status.NextAction, "second") {
		t.Errorf("NextAction = %q, want it to name the phase a resume runs", status.NextAction)
	}

	// The run row itself carries the position: Status reports the record rather
	// than inventing a value.
	run, err := h.store.LoadRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.CurrentPhase == nil || *run.CurrentPhase != "second" {
		t.Errorf("persisted current_phase = %v, want \"second\"", run.CurrentPhase)
	}

	// The pause really landed between the phases: the second phase has not run.
	types := h.eventTypes(result.RunID)
	if indexOfEvent(types, string(domain.EventRunPaused)) < 0 {
		t.Errorf("event log %v has no run.paused", types)
	}
	attempts, err := h.store.Attempts(context.Background(), result.RunID, "second")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("the phase after the interruption ran %d attempt(s), want 0", len(attempts))
	}

	// A resume continues from the recorded position, records run.resumed, and
	// finishes both phases without repeating the one already committed.
	resumed, err := h.engine.Resume(context.Background(), result.RunID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}
	if types = h.eventTypes(result.RunID); indexOfEvent(types, string(domain.EventRunResumed)) < 0 {
		t.Errorf("event log %v has no run.resumed", types)
	}
	if calls := h.client.Calls(); len(calls) != 2 {
		t.Errorf("model calls = %d, want 2 (one per phase; the committed phase is never repeated)", len(calls))
	}
}
