package integration_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
)

// gatedModel answers only once its gate is opened, and observes its call context
// while it waits. It is the one scripted model that must react to cancellation: a
// fan-out branch blocked here is what an interrupted run looks like.
type gatedModel struct {
	arrived int
	reached chan struct{}
	gate    chan struct{}

	mu          sync.Mutex
	arrivedOnce sync.Once
}

func newGatedModel() *gatedModel {
	return &gatedModel{
		reached: make(chan struct{}),
		gate:    make(chan struct{}),
	}
}

// Complete implements the model port.
func (m *gatedModel) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	m.mu.Lock()
	m.arrived++
	arrived := m.arrived
	m.mu.Unlock()
	if arrived >= 2 {
		m.arrivedOnce.Do(func() { close(m.reached) })
	}

	select {
	case <-m.gate:
		return ok("branch output "+req.Metadata["attempt"], 7, 3), nil
	case <-ctx.Done():
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassCancelled,
			Msg:   "branch cancelled while waiting on the provider",
			Err:   ctx.Err(),
		}
	}
}

// open releases every waiting branch; after it returns the model answers normally.
func (m *gatedModel) open() { close(m.gate) }

// TestCancellationPausesMidFanoutThenResumes pins FR-008: cancelling the context
// stops the active branches before the run reaches a terminal state, commits the
// attempt boundary so nothing is half-written, leaves the run paused and resumable,
// and leaks no goroutines. A brand-new call then resumes it to success.
func TestCancellationPausesMidFanoutThenResumes(t *testing.T) {
	model := newGatedModel()
	h := newHarnessWithModel(t, model)

	def := h.workflow("interrupted", func(b *definition.Builder) *definition.Builder {
		return b.WithFanout("draft",
			definition.JoinPolicy{Rule: definition.JoinAll},
			[]definition.Branch{
				definition.BranchOf("outline", agent("outline")),
				definition.BranchOf("details", agent("details")),
			},
			definition.WithOutputs("aggregate"))
	})

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type started struct {
		result engine.RunResult
		err    error
	}
	done := make(chan started, 1)
	go func() {
		result, err := h.engine.Start(ctx, def, engine.StartOptions{Observer: h.observer})
		done <- started{result: result, err: err}
	}()

	// Cancel only once both branches are genuinely in flight, so the test proves
	// the interruption happened mid-fan-out rather than before it started.
	select {
	case <-model.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the fan-out branches never reached the provider")
	}
	cancel()

	var run engine.RunResult
	select {
	case outcome := <-done:
		run = outcome.result
		if outcome.err == nil {
			t.Fatal("Start() returned no error for a cancelled run")
		}
		if class := domain.ClassOf(outcome.err); class != domain.ErrClassCancelled {
			t.Errorf("cancellation error class = %q, want %q", class, domain.ErrClassCancelled)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start() did not return after cancellation; a branch ignored its context")
	}

	if run.Status != domain.RunPaused {
		t.Fatalf("run status = %q, want paused", run.Status)
	}

	// The pause is durable: it is readable by anyone holding only the store, with
	// a recorded reason and the next action naming a resume.
	status, err := h.engine.Status(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Status != domain.RunPaused {
		t.Errorf("stored status = %q, want paused", status.Status)
	}
	if status.NextAction == "" {
		t.Error("a paused run reports no next action")
	}
	// A pause mid-phase records the phase it interrupted as the resume position, so
	// Status names where work stopped (T053).
	if status.CurrentPhase == nil || *status.CurrentPhase != "draft" {
		t.Errorf("paused current_phase = %v, want \"draft\"", status.CurrentPhase)
	}

	types := h.eventTypes(run.RunID)
	if indexOfEvent(types, string(domain.EventRunPaused)) < 0 {
		t.Errorf("event log %v has no run.paused", types)
	}

	// The attempt boundary is committed: both branches have an attempt row and an
	// attempt.started/attempt.abandoned pair, and neither left an artifact behind.
	// Abandoned, not failed: the provider never answered, so the real outcome is
	// unknown and a resume is free to retry the branch.
	attempts, err := h.store.Attempts(context.Background(), run.RunID, "draft")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("committed attempts = %d, want 2 (one per interrupted branch)", len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.Status != domain.AttemptAbandoned {
			t.Errorf("attempt %d status = %q, want abandoned", attempt.AttemptNo, attempt.Status)
		}
	}
	if started := indexOfEvent(types, string(domain.EventAttemptStarted)); started < 0 {
		t.Error("no attempt.started was committed for the interrupted branches")
	}
	if abandoned := indexOfEvent(types, string(domain.EventAttemptAbandoned)); abandoned < 0 {
		t.Error("no attempt.abandoned was committed for the interrupted branches")
	}
	artifacts, err := h.engine.Artifacts(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("cancelled run committed %d artifacts, want none", len(artifacts))
	}

	// No branch goroutine may outlive the cancellation.
	waitForGoroutines(t, baseline)

	// The run is resumable by the same engine; the provider now answers.
	model.open()
	resumed, err := h.engine.Resume(context.Background(), run.RunID, engine.ResumeOptions{Observer: h.observer})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}

	types = h.eventTypes(run.RunID)
	if indexOfEvent(types, string(domain.EventRunResumed)) < 0 {
		t.Errorf("event log %v has no run.resumed", types)
	}

	// Nothing was re-executed: the interrupted attempts stay as they were, and the
	// resumed branches were allocated fresh, monotonic attempt numbers.
	after, err := h.store.Attempts(context.Background(), run.RunID, "draft")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(after) != 4 {
		t.Fatalf("attempts after resume = %d, want 4 (two interrupted plus two resumed)", len(after))
	}
	seen := make(map[int]bool, len(after))
	for _, attempt := range after {
		if seen[attempt.AttemptNo] {
			t.Errorf("attempt number %d was reused", attempt.AttemptNo)
		}
		seen[attempt.AttemptNo] = true
	}

	// And the interrupted branches produced no duplicate side effect: exactly two
	// attempt-owned artifacts (one per resumed branch) plus the single phase-level
	// aggregate, with no artifact left over from the cancelled attempts.
	artifacts, err = h.engine.Artifacts(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	attemptOwned, phaseLevel := 0, 0
	for _, ref := range artifacts {
		if ref.AttemptID == nil {
			phaseLevel++
			if ref.Kind != "aggregate" {
				t.Errorf("phase-level artifact kind = %q, want aggregate", ref.Kind)
			}
		} else {
			attemptOwned++
		}
	}
	if attemptOwned != 2 {
		t.Errorf("attempt-owned artifacts = %d, want 2 (one per resumed branch)", attemptOwned)
	}
	if phaseLevel != 1 {
		t.Errorf("phase-level artifacts = %d, want 1 aggregate", phaseLevel)
	}
}

// waitForGoroutines polls until the goroutine count returns to its baseline, which
// is how a leaked branch goroutine would show up. It fails the test rather than
// hanging when the count never settles.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutines = %d, want no more than the baseline %d", current, baseline)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
