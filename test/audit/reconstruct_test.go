package audit_test

// T057 — reconstruction from the store alone (FR-023, SC-004). A reader who was not
// present during a run must be able to state, from persisted records alone, where the
// run stopped, what it completed, what it still owes, and what the next expected
// action is. This file samples 22 real runs — every shape the increment can produce,
// including a pause mid-fan-out and a failure on an `all` join — and asks the audit
// package the question a fresh reader would ask: given only the store, what happened?
//
// The expectations are written by hand from each scenario's intent, not derived from
// the implementation: that is what makes the check able to fail (Principle III).

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/audit"
	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/test/fakes"
)

// TestVerifyReconstructionFromTheStoreAlone is SC-004: over a corpus of sampled
// scenarios, the reconstruction names the current position, the completed work, the
// pending work, and the next action correctly. The view is handed the store and
// nothing else, so a reader cannot lean on in-memory state only the test holds.
func TestVerifyReconstructionFromTheStoreAlone(t *testing.T) {
	cases := reconstructionCorpus(t)
	if len(cases) < 20 {
		t.Fatalf("the corpus samples %d runs, want at least 20 (SC-004)", len(cases))
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			got, err := audit.Reconstruct(ctx, tc.f.store, tc.runID)
			if err != nil {
				t.Fatalf("Reconstruct() error = %v", err)
			}
			checkReconstruction(t, tc, got)

			// A view is computed on demand, never cached: a second read of the same
			// store cannot disagree with the first.
			again, err := audit.Reconstruct(ctx, tc.f.store, tc.runID)
			if err != nil {
				t.Fatalf("second Reconstruct() error = %v", err)
			}
			if again.Status != got.Status || again.NextAction != got.NextAction ||
				len(again.Completed) != len(got.Completed) || len(again.Pending) != len(got.Pending) {
				t.Errorf("two reads disagreed: %+v then %+v", got, again)
			}
		})
	}
}

// reconstructCase is one sampled run with the reconstruction a fresh reader must
// derive from its store.
type reconstructCase struct {
	name  string
	f     *auditFixture
	runID uuid.UUID
	want  reconstructionWant
}

// reconstructionWant is the hand-written answer for one sampled scenario. An empty
// current phase means the run row records none; an empty reason means the test does
// not constrain it.
type reconstructionWant struct {
	status    domain.RunStatus
	current   string
	reason    string
	completed []audit.Work
	pending   []audit.Work
	next      string
}

// checkReconstruction compares the derived view against the scenario's answer and
// against the engine's own status reader, so the two independent paths cannot drift.
func checkReconstruction(t *testing.T, tc reconstructCase, got audit.Reconstruction) {
	t.Helper()

	if got.RunID != tc.runID {
		t.Errorf("RunID = %s, want %s", got.RunID, tc.runID)
	}
	if got.Status != tc.want.status {
		t.Errorf("Status = %q, want %q", got.Status, tc.want.status)
	}

	switch {
	case tc.want.current == "" && got.CurrentPhase != nil:
		t.Errorf("CurrentPhase = %q, want none", *got.CurrentPhase)
	case tc.want.current != "" && got.CurrentPhase == nil:
		t.Errorf("CurrentPhase = nil, want %q", tc.want.current)
	case tc.want.current != "" && *got.CurrentPhase != tc.want.current:
		t.Errorf("CurrentPhase = %q, want %q", *got.CurrentPhase, tc.want.current)
	}

	if tc.want.reason != "" {
		if got.Reason == nil {
			t.Errorf("Reason = nil, want it to name %q", tc.want.reason)
		} else if !strings.Contains(*got.Reason, tc.want.reason) {
			t.Errorf("Reason = %q, want it to name %q", *got.Reason, tc.want.reason)
		}
	}

	compareWork(t, "completed", tc.want.completed, got.Completed)
	compareWork(t, "pending", tc.want.pending, got.Pending)

	if got.NextAction != tc.want.next {
		t.Errorf("NextAction = %q, want %q", got.NextAction, tc.want.next)
	}

	// The audit view and the engine's status reader are two readers of one record;
	// a reader told one next action by `audit` and another by `Engine.Status` would
	// have to distrust both.
	status, err := tc.f.engine.Status(context.Background(), tc.runID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.NextAction != got.NextAction {
		t.Errorf("Engine.Status NextAction = %q, audit says %q", status.NextAction, got.NextAction)
	}
	if !samePhase(status.CurrentPhase, got.CurrentPhase) {
		t.Errorf("Engine.Status CurrentPhase = %v, audit says %v", status.CurrentPhase, got.CurrentPhase)
	}
}

// compareWork asserts one work list exactly, naming the first difference.
func compareWork(t *testing.T, label string, want, got []audit.Work) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s = %s, want %s", label, describeWork(got), describeWork(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %s, want %s", label, i, describeWork(got[i:i+1]), describeWork(want[i:i+1]))
		}
	}
}

// describeWork renders a work list for a failure message.
func describeWork(work []audit.Work) string {
	if len(work) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(work))
	for _, item := range work {
		name := item.Phase
		if item.Branch != "" {
			name += "/" + item.Branch
		}
		parts = append(parts, name+":"+item.Status)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// samePhase reports whether two optional phase names name the same position.
func samePhase(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// reconstructionCorpus samples every shape the increment can commit. Each builder
// drives a real engine over a real store; only the model is scripted.
func reconstructionCorpus(t *testing.T) []reconstructCase {
	t.Helper()

	return []reconstructCase{
		singlePhaseSuccess(t),
		orderedPhasesSuccess(t),
		threePhasesSuccess(t),
		fanoutAllSuccess(t),
		fanoutAnySuccess(t),
		fanoutNofMSuccess(t),
		fanoutAllFailed(t),
		fanoutSkipped(t),
		retryThenSuccess(t),
		failureAfterRetries(t),
		degradedContinue(t),
		skippedPhase(t),
		budgetStopContinue(t),
		budgetStopAbort(t),
		pausedBetweenPhases(t),
		pausedMidPhase(t),
		pausedMidFanout(t),
		resumedAfterPause(t),
		resumedAfterRestart(t),
		resumedFanout(t),
		takeoverWhilePaused(t),
		driftOverride(t),
	}
}

func singlePhaseSuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	def := f.workflow("single", func(b *definition.Builder) *definition.Builder {
		return b.WithPhase("only", agent("only"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "single_phase_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:    domain.RunSucceeded,
			current:   "only",
			reason:    "all phases reached a terminal state",
			completed: []audit.Work{phaseWork("only", domain.PhaseSucceeded)},
			next:      "nothing; the run is complete",
		},
	}
}

func orderedPhasesSuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	def := f.workflow("ordered", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first")).
			WithPhase("second", agent("second"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "ordered_phases_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "second",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func threePhasesSuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	def := f.workflow("three", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("draft", agent("draft")).
			WithPhase("edit", agent("edit")).
			WithPhase("publish", agent("publish"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "three_phases_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "publish",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("draft", domain.PhaseSucceeded),
				phaseWork("edit", domain.PhaseSucceeded),
				phaseWork("publish", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func fanoutAllSuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.runSucceeds(f.threePhaseWorkflow())

	return reconstructCase{
		name: "fanout_all_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "review",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("plan", domain.PhaseSucceeded),
				phaseWork("build", domain.PhaseSucceeded),
				branchWork("build", "one", domain.AttemptSucceeded),
				branchWork("build", "two", domain.AttemptSucceeded),
				branchWork("build", "three", domain.AttemptSucceeded),
				phaseWork("review", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func fanoutAnySuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, failingBranch("two"))
	def := f.workflow("fanout-any", func(b *definition.Builder) *definition.Builder {
		return b.
			WithFanout("gather", definition.JoinPolicy{Rule: definition.JoinAny}, branches("one", "two", "three")).
			WithPhase("after", agent("after"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "fanout_any_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "after",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("gather", domain.PhaseSucceeded),
				branchWork("gather", "one", domain.AttemptSucceeded),
				branchWork("gather", "two", domain.AttemptFailed),
				branchWork("gather", "three", domain.AttemptSucceeded),
				phaseWork("after", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func fanoutNofMSuccess(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, failingBranch("two"))
	def := f.workflow("fanout-n-of-m", func(b *definition.Builder) *definition.Builder {
		enough := 2
		return b.
			WithFanout("gather", definition.JoinPolicy{Rule: definition.JoinNofM, N: &enough}, branches("one", "two", "three")).
			WithPhase("after", agent("after"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "fanout_n_of_m_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "after",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("gather", domain.PhaseSucceeded),
				branchWork("gather", "one", domain.AttemptSucceeded),
				branchWork("gather", "two", domain.AttemptFailed),
				branchWork("gather", "three", domain.AttemptSucceeded),
				phaseWork("after", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

// fanoutAllFailed is the `all` join that fails: one branch failed, so the phase
// failed, the run stopped, and the phase after it never ran. A reader must see both
// the completed branches and the work the run still owed.
func fanoutAllFailed(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, failingBranch("two"))
	def := f.workflow("fanout-all-fails", func(b *definition.Builder) *definition.Builder {
		return b.
			WithFanout("gather", definition.JoinPolicy{Rule: definition.JoinAll}, branches("one", "two", "three")).
			WithPhase("after", agent("after"))
	})
	run := f.runFails(def)

	return reconstructCase{
		name: "fanout_all_failed", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunFailed,
			current: "gather",
			reason:  "gather",
			completed: []audit.Work{
				phaseWork("gather", domain.PhaseFailed),
				branchWork("gather", "one", domain.AttemptSucceeded),
				branchWork("gather", "two", domain.AttemptFailed),
				branchWork("gather", "three", domain.AttemptSucceeded),
			},
			pending: []audit.Work{phaseWork("after", domain.PhasePending)},
			next:    "inspect the failed phase, then resume after fixing its cause",
		},
	}
}

// fanoutSkipped is a fan-out whose when-condition did not hold: the phase is
// terminal without ever running a branch, so it has no branch work at all.
func fanoutSkipped(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, failingBranch("first"))
	def := f.workflow("fanout-skipped", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first"), definition.OnFailure(definition.FailContinue)).
			WithFanout("fan", definition.JoinPolicy{Rule: definition.JoinAll}, branches("one", "two"),
				definition.When("first", definition.OutcomeSucceeded))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "fanout_skipped", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "first",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseDegraded),
				phaseWork("fan", domain.PhaseSkipped),
			},
			next: "nothing; the run is complete",
		},
	}
}

func retryThenSuccess(t *testing.T) reconstructCase {
	t.Helper()

	var calls atomic.Int32
	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		if calls.Add(1) == 1 {
			return domain.ModelResponse{}, transientFailure("the first attempt fails on purpose")
		}
		return okResponse("recovered"), nil
	}))
	def := f.workflow("retrying", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("flaky", agent("flaky"), definition.WithRetry(definition.Retry{MaxAttempts: 2})).
			WithPhase("after", agent("after"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "retry_then_success", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "after",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("flaky", domain.PhaseSucceeded),
				phaseWork("after", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func failureAfterRetries(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{}, transientFailure("the provider never recovers in this scenario")
	}))
	def := f.workflow("retries-exhausted", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("doomed", agent("doomed"), definition.WithRetry(definition.Retry{MaxAttempts: 2})).
			WithPhase("after", agent("after"))
	})
	run := f.runFails(def)

	return reconstructCase{
		name: "failure_after_retries", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:    domain.RunFailed,
			current:   "doomed",
			reason:    "doomed",
			completed: []audit.Work{phaseWork("doomed", domain.PhaseFailed)},
			pending:   []audit.Work{phaseWork("after", domain.PhasePending)},
			next:      "inspect the failed phase, then resume after fixing its cause",
		},
	}
}

func degradedContinue(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["phase"] == "flaky" {
			return domain.ModelResponse{}, permanentFailure("flaky fails on purpose")
		}
		return okResponse("output of " + req.Metadata["attempt"]), nil
	}))
	def := f.workflow("degraded", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("flaky", agent("flaky"), definition.OnFailure(definition.FailContinue)).
			WithPhase("after", agent("after"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "degraded_continue", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "after",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("flaky", domain.PhaseDegraded),
				phaseWork("after", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func skippedPhase(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["phase"] == "first" {
			return domain.ModelResponse{}, permanentFailure("first fails so the condition cannot hold")
		}
		return okResponse("output of " + req.Metadata["attempt"]), nil
	}))
	def := f.workflow("skipping", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first"), definition.OnFailure(definition.FailContinue)).
			WithPhase("compensate", agent("compensate"), definition.When("first", definition.OutcomeSucceeded)).
			WithPhase("report", agent("report"))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "skipped_phase", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "report",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseDegraded),
				phaseWork("compensate", domain.PhaseSkipped),
				phaseWork("report", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func budgetStopContinue(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, spendingModel())
	tokens := 10
	def := f.workflow("budget-continue", func(b *definition.Builder) *definition.Builder {
		return b.
			WithBudget(domain.Budget{Tokens: &tokens}).
			WithPhase("expensive", agent("expensive")).
			WithPhase("second", agent("second"), definition.OnFailure(definition.FailContinue))
	})
	run := f.runSucceeds(def)

	return reconstructCase{
		name: "budget_stop_continue", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "second",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("expensive", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseDegraded),
			},
			next: "nothing; the run is complete",
		},
	}
}

func budgetStopAbort(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, spendingModel())
	tokens := 10
	def := f.workflow("budget-abort", func(b *definition.Builder) *definition.Builder {
		return b.
			WithBudget(domain.Budget{Tokens: &tokens}).
			WithPhase("expensive", agent("expensive")).
			WithPhase("second", agent("second"))
	})
	run := f.runFails(def)

	return reconstructCase{
		name: "budget_stop_abort", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunFailed,
			current: "second",
			reason:  "second",
			completed: []audit.Work{
				phaseWork("expensive", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseFailed),
			},
			next: "inspect the failed phase, then resume after fixing its cause",
		},
	}
}

func pausedBetweenPhases(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.pauseBetweenPhases(twoPhaseDefinition(f))

	return reconstructCase{
		name: "paused_between_phases", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:    domain.RunPaused,
			current:   "second",
			reason:    "context ended",
			completed: []audit.Work{phaseWork("first", domain.PhaseSucceeded)},
			pending:   []audit.Work{phaseWork("second", domain.PhasePending)},
			next:      "resume the run to execute phase second",
		},
	}
}

// pausedMidPhase stops the run inside a single-agent attempt: the phase row stays
// running, so the reader must report an interrupted phase rather than a completed
// one, and the work after it as pending.
func pausedMidPhase(t *testing.T) reconstructCase {
	t.Helper()

	gate := newAttemptGate()
	f := newAuditFixtureWithModel(t, &blockingModel{gate: gate})
	run := f.pauseInsideAttempt(twoPhaseDefinition(f), gate)

	return reconstructCase{
		name: "paused_mid_phase", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:  domain.RunPaused,
			current: "first",
			reason:  "cancelled during phase first",
			pending: []audit.Work{
				phaseWork("first", domain.PhaseRunning),
				phaseWork("second", domain.PhasePending),
			},
			next: "resume the run; phase first was interrupted",
		},
	}
}

// pausedMidFanout is the sampling SC-004 names: a fan-out interrupted with one
// branch committed and one still inside its attempt. The committed branch must be
// visible as completed work and the blocked one as pending work of a running phase.
func pausedMidFanout(t *testing.T) reconstructCase {
	t.Helper()

	gate := newAttemptGate()
	f := newAuditFixtureWithModel(t, &fanoutGateModel{gate: gate, branch: "two"})
	run := f.pauseInsideFanout(fanoutDefinition(f), gate)

	return reconstructCase{
		name: "paused_mid_fanout", f: f, runID: run.RunID,
		want: reconstructionWant{
			status:    domain.RunPaused,
			current:   "build",
			reason:    "cancelled during phase build",
			completed: []audit.Work{branchWork("build", "one", domain.AttemptSucceeded)},
			pending: []audit.Work{
				phaseWork("build", domain.PhaseRunning),
				pendingBranch("build", "two"),
				phaseWork("review", domain.PhasePending),
			},
			next: "resume the run; phase build was interrupted",
		},
	}
}

func resumedAfterPause(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	paused := f.pauseBetweenPhases(twoPhaseDefinition(f))
	resumed, err := f.engine.Resume(context.Background(), paused.RunID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}

	return reconstructCase{
		name: "resumed_after_pause", f: f, runID: paused.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "second",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

// resumedAfterRestart reconstructs a run that was interrupted mid-attempt and then
// resumed: the abandoned attempt is replaced, and the work reads as completed once
// the restarted attempt commits.
func resumedAfterRestart(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	paused := f.pauseBetweenPhases(twoPhaseDefinition(f))
	f.simulateRunningAttempt(paused.RunID, "second", 1, "second")

	resumed, err := f.engine.Resume(context.Background(), paused.RunID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}

	return reconstructCase{
		name: "resumed_after_restart", f: f, runID: paused.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "second",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

// resumedFanout finishes a fan-out interrupted after one branch committed. The
// committed branch is reused, the blocked one runs, and the reconstruction must end
// with the whole phase completed.
func resumedFanout(t *testing.T) reconstructCase {
	t.Helper()

	gate := newAttemptGate()
	f := newAuditFixtureWithModel(t, &fanoutGateModel{gate: gate, branch: "two"})
	paused := f.pauseInsideFanout(fanoutDefinition(f), gate)

	resumed, err := f.engine.Resume(context.Background(), paused.RunID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}

	return reconstructCase{
		name: "resumed_fanout", f: f, runID: paused.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "review",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("build", domain.PhaseSucceeded),
				branchWork("build", "one", domain.AttemptSucceeded),
				branchWork("build", "two", domain.AttemptSucceeded),
				phaseWork("review", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

// takeoverWhilePaused changes ownership without advancing the run: the position a
// reader reports must be the same one the pause recorded.
func takeoverWhilePaused(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	paused := f.pauseBetweenPhases(twoPhaseDefinition(f))

	ctx := context.Background()
	if _, err := f.store.AcquireLease(ctx, paused.RunID, "audit-first", 20*time.Millisecond); err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := f.store.AcquireLease(ctx, paused.RunID, "audit-second", time.Second); err != nil {
		t.Fatalf("takeover AcquireLease() error = %v", err)
	}

	return reconstructCase{
		name: "takeover_while_paused", f: f, runID: paused.RunID,
		want: reconstructionWant{
			status:    domain.RunPaused,
			current:   "second",
			reason:    "context ended",
			completed: []audit.Work{phaseWork("first", domain.PhaseSucceeded)},
			pending:   []audit.Work{phaseWork("second", domain.PhasePending)},
			next:      "resume the run to execute phase second",
		},
	}
}

// driftOverride resumes a paused run under a definition whose fingerprint no longer
// matches, accepted explicitly. The run still executes its recorded snapshot, so the
// reconstruction is the ordinary completed one.
func driftOverride(t *testing.T) reconstructCase {
	t.Helper()

	f := newAuditFixture(t)
	paused := f.pauseBetweenPhases(twoPhaseDefinition(f))

	drifted := f.workflow("reconstruct-two-phases", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", definition.Agent("first", "a different instruction after the fact", "gpt-4.1-mini", 256)).
			WithPhase("second", agent("second"))
	})
	resumed, err := f.engine.Resume(context.Background(), paused.RunID, engine.ResumeOptions{
		Definition:           &drifted,
		AllowDefinitionDrift: true,
	})
	if err != nil {
		t.Fatalf("Resume() with an allowed drift error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}

	return reconstructCase{
		name: "drift_override", f: f, runID: paused.RunID,
		want: reconstructionWant{
			status:  domain.RunSucceeded,
			current: "second",
			reason:  "all phases reached a terminal state",
			completed: []audit.Work{
				phaseWork("first", domain.PhaseSucceeded),
				phaseWork("second", domain.PhaseSucceeded),
			},
			next: "nothing; the run is complete",
		},
	}
}

// twoPhaseDefinition is the ordered pair the pause scenarios interrupt.
func twoPhaseDefinition(f *auditFixture) definition.Definition {
	return f.workflow("reconstruct-two-phases", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first")).
			WithPhase("second", agent("second"))
	})
}

// fanoutDefinition is a two-branch fan-out followed by a single phase, so a pause
// inside the fan-out has both branch work and later work to report.
func fanoutDefinition(f *auditFixture) definition.Definition {
	return f.workflow("reconstruct-fanout", func(b *definition.Builder) *definition.Builder {
		return b.
			WithFanout("build", definition.JoinPolicy{Rule: definition.JoinAll}, branches("one", "two")).
			WithPhase("review", agent("review"))
	})
}

// pauseBetweenPhases stops a run after its first phase reaches a terminal state.
func (f *auditFixture) pauseBetweenPhases(def definition.Definition) engine.RunResult {
	f.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := f.engine.Start(ctx, def, engine.StartOptions{
		Observer: &cancelAfterPhase{Observer: fakes.NewObserver(), phase: "first", cancel: cancel},
	})
	if err == nil {
		f.t.Fatal("Start() returned no error for an interrupted run")
	}
	if result.Status != domain.RunPaused {
		f.t.Fatalf("interrupted run status = %q, want paused", result.Status)
	}
	return result
}

// pauseInsideAttempt cancels a run while a single-agent attempt is blocked in the
// model. The gate signals the attempt started, and that commit precedes the call, so
// the cancellation lands inside a durable attempt rather than racing it.
func (f *auditFixture) pauseInsideAttempt(def definition.Definition, gate *attemptGate) engine.RunResult {
	f.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan engine.RunResult, 1)
	go func() {
		result, _ := f.engine.Start(ctx, def, engine.StartOptions{})
		done <- result
	}()

	select {
	case <-gate.started:
	case <-time.After(2 * time.Second):
		f.t.Fatal("the run never reached the blocked attempt")
	}
	cancel()

	select {
	case result := <-done:
		if result.Status != domain.RunPaused {
			f.t.Fatalf("interrupted run status = %q, want paused", result.Status)
		}
		return result
	case <-time.After(2 * time.Second):
		f.t.Fatal("the interrupted run did not stop after cancellation")
		return engine.RunResult{}
	}
}

// pauseInsideFanout cancels a fan-out once one branch is committed and another is
// blocked inside its attempt. It waits on the durable record for the committed
// branch, so the interruption point is observed rather than assumed.
func (f *auditFixture) pauseInsideFanout(def definition.Definition, gate *attemptGate) engine.RunResult {
	f.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	observer := newRunIDReporter()
	done := make(chan engine.RunResult, 1)
	go func() {
		result, _ := f.engine.Start(ctx, def, engine.StartOptions{Observer: observer})
		done <- result
	}()

	var runID uuid.UUID
	select {
	case runID = <-observer.id:
	case <-time.After(2 * time.Second):
		f.t.Fatal("the run never started a phase")
	}

	select {
	case <-gate.started:
	case <-time.After(2 * time.Second):
		f.t.Fatal("the fan-out never reached the blocked branch")
	}
	f.waitForAttempt(runID, "build", "one", domain.AttemptSucceeded)
	cancel()

	select {
	case result := <-done:
		if result.Status != domain.RunPaused {
			f.t.Fatalf("interrupted run status = %q, want paused", result.Status)
		}
		return result
	case <-time.After(2 * time.Second):
		f.t.Fatal("the interrupted run did not stop after cancellation")
		return engine.RunResult{}
	}
}

// runSucceeds records a run that must reach `succeeded`.
func (f *auditFixture) runSucceeds(def definition.Definition) engine.RunResult {
	f.t.Helper()

	result := f.record(def)
	if result.Status != domain.RunSucceeded {
		f.t.Fatalf("run status = %q, want succeeded", result.Status)
	}
	return result
}

// runFails records a run that must reach `failed`.
func (f *auditFixture) runFails(def definition.Definition) engine.RunResult {
	f.t.Helper()

	result, err := f.engine.Start(context.Background(), def, engine.StartOptions{})
	if err == nil {
		f.t.Fatal("Start() returned no error for a failed run")
	}
	if result.Status != domain.RunFailed {
		f.t.Fatalf("run status = %q, want failed", result.Status)
	}
	return result
}

// waitForAttempt waits until the store holds an attempt of the given agent in the
// given state, so a test can act on a fact rather than on elapsed time.
func (f *auditFixture) waitForAttempt(runID uuid.UUID, phase, agentName string, status domain.AttemptStatus) {
	f.t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		attempts, err := f.store.Attempts(context.Background(), runID, phase)
		if err != nil {
			f.t.Fatalf("Attempts(%q) error = %v", phase, err)
		}
		for _, attempt := range attempts {
			if attempt.AgentName == agentName && attempt.Status == status {
				return
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("run %s never recorded a %s attempt for %q/%q", runID, status, phase, agentName)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// attemptGate signals that a blocking attempt has started; the start is already
// durable when the model is called, so the signal marks a real interruption point.
type attemptGate struct {
	once    sync.Once
	started chan struct{}
}

func newAttemptGate() *attemptGate {
	return &attemptGate{started: make(chan struct{})}
}

func (g *attemptGate) mark() {
	g.once.Do(func() { close(g.started) })
}

// blockingModel blocks every call until the run's context ends, which is the state a
// pause interrupts inside a single-agent phase.
type blockingModel struct{ gate *attemptGate }

func (m *blockingModel) Complete(ctx context.Context, _ domain.ModelRequest) (domain.ModelResponse, error) {
	m.gate.mark()
	<-ctx.Done()
	return domain.ModelResponse{}, &domain.Error{
		Class: domain.ErrClassCancelled,
		Msg:   "cancelled inside the attempt",
		Err:   ctx.Err(),
	}
}

// fanoutGateModel answers every branch except the named one, which it blocks on its
// first call only. A resumed run therefore gets a real answer and can finish.
type fanoutGateModel struct {
	gate   *attemptGate
	branch string
	block  atomic.Bool
}

func (m *fanoutGateModel) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	if req.Metadata["attempt"] == m.branch && m.block.CompareAndSwap(false, true) {
		m.gate.mark()
		<-ctx.Done()
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassCancelled,
			Msg:   "cancelled inside the branch",
			Err:   ctx.Err(),
		}
	}
	return okResponse("output of " + req.Metadata["attempt"]), nil
}

// runIDReporter reports the run identifier as soon as the engine starts its first
// phase, which is the earliest moment the run exists durably.
type runIDReporter struct {
	*fakes.Observer
	once sync.Once
	id   chan uuid.UUID
}

func newRunIDReporter() *runIDReporter {
	return &runIDReporter{Observer: fakes.NewObserver(), id: make(chan uuid.UUID, 1)}
}

func (r *runIDReporter) OnPhaseStarted(ctx context.Context, runID uuid.UUID, phase string) {
	r.Observer.OnPhaseStarted(ctx, runID, phase)
	r.once.Do(func() { r.id <- runID })
}

// spendingModel consumes the whole token allowance in one call.
func spendingModel() *fakes.FuncClient {
	return fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{
			Content:    "spendy",
			Usage:      domain.Usage{InputTokens: 6, OutputTokens: 4},
			StopReason: domain.StopEnd,
		}, nil
	})
}

// failingBranch fails one named branch permanently and answers the rest.
func failingBranch(branch string) *fakes.FuncClient {
	return fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["attempt"] == branch {
			return domain.ModelResponse{}, permanentFailure("branch " + branch + " fails on purpose")
		}
		return okResponse("output of " + req.Metadata["attempt"]), nil
	})
}

// branches builds one agent per branch, named after it.
func branches(names ...string) []definition.Branch {
	out := make([]definition.Branch, 0, len(names))
	for _, name := range names {
		out = append(out, definition.BranchOf(name, agent(name)))
	}
	return out
}

// phaseWork names a phase-level work item.
func phaseWork(phase string, status domain.PhaseStatus) audit.Work {
	return audit.Work{Phase: phase, Status: string(status)}
}

// branchWork names a fan-out branch with the outcome the record gives it.
func branchWork(phase, branch string, status domain.AttemptStatus) audit.Work {
	return audit.Work{Phase: phase, Branch: branch, Status: string(status)}
}

// pendingBranch names a branch that has no outcome yet.
func pendingBranch(phase, branch string) audit.Work {
	return audit.Work{Phase: phase, Branch: branch, Status: string(domain.PhasePending)}
}
