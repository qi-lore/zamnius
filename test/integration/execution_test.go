package integration_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
)

// TestFanoutBranchesOverlap is SC-006: three branches must genuinely overlap, and
// the phase must finish in about the time of one branch rather than three.
func TestFanoutBranchesOverlap(t *testing.T) {
	t.Parallel()

	// The branch delay sets the signal-to-noise ratio of the comparison below. Every
	// run pays a fixed cost that does not shrink with the delay — definition
	// snapshot, lease, attempt and artifact commits, and the join for a fan-out — so
	// the delay has to dominate that cost for the ratio to measure overlap rather
	// than per-run overhead. At 150ms the two runs differ by ~116ms of fixed cost on
	// a shared CI runner under -race, which is 77% of the single-branch baseline;
	// that is what tripped the 1.5x bound at 1.51x. At 1s the same noise is ~11% of
	// the measurement, while a serialized fan-out still lands near 3x, so the bound
	// keeps its teeth in both directions.
	const delay = time.Second

	// One branch, measured in the same test so the comparison cannot drift with
	// machine speed.
	single := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		time.Sleep(delay)
		return ok("single", 3, 1), nil
	})
	singleDef := single.workflow("single", func(b *definition.Builder) *definition.Builder {
		return b.WithPhase("solo", agent("solo"), definition.WithOutputs("report"))
	})

	start := time.Now()
	single.run(singleDef)
	singleElapsed := time.Since(start)

	parallel := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		time.Sleep(delay)
		return ok("parallel", 3, 1), nil
	})
	parallelDef := parallel.workflow("parallel", func(b *definition.Builder) *definition.Builder {
		return b.WithFanout("fan",
			definition.JoinPolicy{Rule: definition.JoinAll},
			[]definition.Branch{
				definition.BranchOf("alpha", agent("alpha")),
				definition.BranchOf("beta", agent("beta")),
				definition.BranchOf("gamma", agent("gamma")),
			},
			definition.WithOutputs("report"))
	})

	start = time.Now()
	result := parallel.run(parallelDef)
	parallelElapsed := time.Since(start)

	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	// The baseline must be a real wall-clock measurement of at least the branch
	// delay, or the comparison below is vacuous.
	if singleElapsed < delay {
		t.Fatalf("single-branch baseline %v is below the branch delay %v; the run is not measuring wall clock",
			singleElapsed, delay)
	}
	// Three concurrent branches must cost about one branch, not three: 1.5x is the
	// budget SC-006 sets.
	if parallelElapsed > singleElapsed*3/2 {
		t.Errorf("fan-out took %v versus %v for one branch; branches did not overlap",
			parallelElapsed, singleElapsed)
	}

	// The stored timestamps must show the overlap, so the claim is auditable rather
	// than incidental.
	attempts, err := parallel.store.Attempts(context.Background(), result.RunID, "fan")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(attempts))
	}
	overlapping := 0
	for i := range attempts {
		for j := i + 1; j < len(attempts); j++ {
			left, right := attempts[i], attempts[j]
			if left.EndedAt == nil || right.EndedAt == nil {
				t.Fatalf("attempt %d or %d has no end time", i, j)
			}
			if left.StartedAt.Before(*right.EndedAt) && right.StartedAt.Before(*left.EndedAt) {
				overlapping++
			}
		}
	}
	if overlapping == 0 {
		t.Errorf("no pair of branch attempts overlapped: %+v", attempts)
	}
}

// TestRetrySucceedsOnThirdAttempt covers retry accounting and the guarantee that
// downstream phases run exactly once.
func TestRetrySucceedsOnThirdAttempt(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	attempts := make(map[string]int)
	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		mu.Lock()
		attempts[req.Metadata["phase"]]++
		count := attempts[req.Metadata["phase"]]
		mu.Unlock()

		if req.Metadata["phase"] == "flaky" && count < 3 {
			return domain.ModelResponse{}, fakesError(domain.ErrClassTransient, "temporary failure")
		}
		return ok("done "+req.Metadata["phase"], 7, 3), nil
	})

	retry := definition.Retry{MaxAttempts: 3}
	def := h.workflow("retrying", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("flaky", agent("flaky"), definition.WithRetry(retry)).
			WithPhase("after", agent("after"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	stored, err := h.store.Attempts(context.Background(), result.RunID, "flaky")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("attempts of flaky = %d, want 3", len(stored))
	}
	if stored[2].Status != domain.AttemptSucceeded {
		t.Errorf("third attempt status = %q, want succeeded", stored[2].Status)
	}
	if stored[0].ErrorClass != domain.ErrClassTransient {
		t.Errorf("first attempt class = %q, want transient", stored[0].ErrorClass)
	}

	downstream, err := h.store.Attempts(context.Background(), result.RunID, "after")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(downstream) != 1 {
		t.Fatalf("downstream attempts = %d, want exactly 1", len(downstream))
	}

	// The run's recorded spend must be real: four attempts ran (three flaky plus
	// one after). The two failed flaky attempts reported no usage — a provider
	// that fails returns no token counts — so only the two successes contribute
	// tokens, while all four contribute an attempt and a duration. A run whose
	// spent counter stayed zero would make every budget decision blind.
	run, err := h.store.LoadRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Spent.Attempts != 4 {
		t.Errorf("spent attempts = %d, want 4", run.Spent.Attempts)
	}
	if run.Spent.TokensIn != 14 || run.Spent.TokensOut != 6 {
		t.Errorf("spent tokens = %d in / %d out, want 14 in / 6 out (two successful attempts)",
			run.Spent.TokensIn, run.Spent.TokensOut)
	}
	if run.Spent.WallClockMS <= 0 {
		t.Errorf("spent wall clock = %d, want a positive duration", run.Spent.WallClockMS)
	}
	if report, err := h.engine.Status(context.Background(), result.RunID); err != nil {
		t.Fatalf("Status() error = %v", err)
	} else if report.Spent != run.Spent {
		t.Errorf("status spend = %+v, want the committed %+v", report.Spent, run.Spent)
	}

	// Every retry must be explained, naming the class that triggered it.
	decisions, err := h.store.Decisions(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	retries := 0
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionRetry {
			retries++
			if !strings.Contains(decision.Rationale, "transient") {
				t.Errorf("retry rationale = %q, want it to name the class", decision.Rationale)
			}
		}
	}
	if retries != 2 {
		t.Fatalf("retry decisions = %d, want 2", retries)
	}
}

// TestTokenBudgetExhaustionStopsThePhase is US1 scenario 8: the recorded usage is
// real, the exhausted phase stops before calling the model again, and the stop
// names the unit that ran out.
func TestTokenBudgetExhaustionStopsThePhase(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		// Exactly the whole allowance in one call, so the second phase cannot start.
		return ok("spendy", 6, 4), nil
	})

	tokens := 10
	def := h.workflow("budget", func(b *definition.Builder) *definition.Builder {
		return b.
			WithBudget(domain.Budget{Tokens: &tokens}).
			WithPhase("expensive", agent("expensive")).
			WithPhase("second", agent("second"), definition.OnFailure(definition.FailContinue))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded (continue policy)", result.Status)
	}
	if calls := len(h.client.Calls()); calls != 1 {
		t.Fatalf("model calls = %d, want 1: the exhausted phase must not call the model", calls)
	}

	types := h.eventTypes(result.RunID)
	if indexOfEvent(types, string(domain.EventPhaseBudgetExhausted)) < 0 {
		t.Fatalf("no phase.budget_exhausted event: %v", types)
	}

	events := h.events(result.RunID)
	var budgetEvent *domain.Event
	for i := range events {
		if events[i].Type == string(domain.EventPhaseBudgetExhausted) {
			budgetEvent = &events[i]
		}
	}
	if budgetEvent == nil || !strings.Contains(string(budgetEvent.Payload), "tokens") {
		t.Fatalf("budget event does not name tokens: %v", budgetEvent)
	}
	if budgetEvent.Phase == nil || *budgetEvent.Phase != "second" {
		t.Errorf("budget event phase = %v, want the phase that could not start", budgetEvent.Phase)
	}

	decisions, err := h.store.Decisions(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	var stop *domain.Decision
	for i := range decisions {
		if decisions[i].Kind == domain.DecisionBudgetStop {
			stop = &decisions[i]
		}
	}
	if stop == nil {
		t.Fatalf("no budget_stop decision: %+v", decisions)
	}
	if !strings.Contains(string(stop.Evidence), "tokens") {
		t.Errorf("budget decision evidence = %s, want the exhausted unit", stop.Evidence)
	}

	phases, err := h.store.Phases(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	statuses := map[string]domain.PhaseStatus{}
	for _, phase := range phases {
		statuses[phase.Name] = phase.Status
	}
	if statuses["expensive"] != domain.PhaseSucceeded {
		t.Errorf("first phase = %q, want succeeded", statuses["expensive"])
	}
	if statuses["second"] != domain.PhaseDegraded {
		t.Errorf("second phase = %q, want degraded under the continue policy", statuses["second"])
	}
}

// TestFailurePolicyContinueDegradesAndAbortStops pins both policies, because the
// difference between them is the whole reason a policy exists.
func TestFailurePolicyContinueDegradesAndAbortStops(t *testing.T) {
	t.Parallel()

	t.Run("continue", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
			if req.Metadata["phase"] == "first" {
				return domain.ModelResponse{}, fakesError(domain.ErrClassAuth, "bad key")
			}
			return ok("second", 2, 1), nil
		})
		def := h.workflow("continue-policy", func(b *definition.Builder) *definition.Builder {
			return b.
				WithPhase("first", agent("first"), definition.OnFailure(definition.FailContinue)).
				WithPhase("second", agent("second"))
		})

		result := h.run(def)
		if result.Status != domain.RunSucceeded {
			t.Fatalf("status = %q, want succeeded", result.Status)
		}
		phases, err := h.store.Phases(context.Background(), result.RunID)
		if err != nil {
			t.Fatalf("Phases() error = %v", err)
		}
		if phases[0].Status != domain.PhaseDegraded || !phases[0].Degraded {
			t.Fatalf("first phase = %+v, want degraded", phases[0])
		}
		if indexOfEvent(h.eventTypes(result.RunID), string(domain.EventPhaseDegraded)) < 0 {
			t.Error("no phase.degraded event")
		}
	})

	t.Run("abort", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
			return domain.ModelResponse{}, fakesError(domain.ErrClassAuth, "bad key")
		})
		def := h.workflow("abort-policy", func(b *definition.Builder) *definition.Builder {
			return b.
				WithPhase("first", agent("first"), definition.OnFailure(definition.FailAbort)).
				WithPhase("second", agent("second"))
		})

		result, err := h.engine.Start(context.Background(), def, engine.StartOptions{})
		if err == nil {
			t.Fatal("Start() returned no error for an aborting phase")
		}
		if result.Status != domain.RunFailed {
			t.Errorf("status = %q, want failed", result.Status)
		}
		// The second phase must not have run at all.
		if calls := h.client.CallCount("phase", "second"); calls != 0 {
			t.Errorf("second phase ran %d times after an abort", calls)
		}
		if indexOfEvent(h.eventTypes(result.RunID), string(domain.EventRunFailed)) < 0 {
			t.Error("no run.failed event")
		}
	})
}

// TestProvenanceAndDeclaredOutputs covers the artifact invariants: every row names
// its run, phase and attempt, and a phase that promises an output kind must have
// committed one.
func TestProvenanceAndDeclaredOutputs(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return ok("content of "+req.Metadata["attempt"], 4, 2), nil
	})

	def := h.workflow("provenance", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("plan", agent("plan"), definition.WithOutputs("plan")).
			WithFanout("review",
				definition.JoinPolicy{Rule: definition.JoinAll},
				[]definition.Branch{
					definition.BranchOf("alpha", agent("alpha")),
					definition.BranchOf("beta", agent("beta")),
				},
				definition.WithOutputs("evidence"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	refs, err := h.engine.Artifacts(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(refs) < 4 {
		t.Fatalf("artifacts = %d, want at least 4 (plan, two branches, aggregate)", len(refs))
	}

	kinds := make(map[string]int)
	for _, ref := range refs {
		kinds[ref.Kind]++
		if ref.RunID != result.RunID {
			t.Errorf("artifact %s belongs to %s, want %s", ref.SHA256, ref.RunID, result.RunID)
		}
		if ref.Phase == "" {
			t.Errorf("artifact %s has no phase", ref.SHA256)
		}
		if ref.Kind == "" || ref.MediaType == "" || ref.SizeBytes <= 0 {
			t.Errorf("artifact %s is incomplete: %+v", ref.SHA256, ref)
		}
	}
	if kinds["plan"] != 1 {
		t.Errorf("plan artifacts = %d, want 1", kinds["plan"])
	}
	if kinds["aggregate"] != 1 {
		t.Errorf("aggregate artifacts = %d, want 1", kinds["aggregate"])
	}

	// Every declared output kind of a succeeded phase must have a committed row.
	for _, phase := range result.Phases {
		if phase.Status != domain.PhaseSucceeded {
			continue
		}
		if len(phase.Artifacts) == 0 {
			t.Errorf("succeeded phase %q has no artifacts", phase.Name)
		}
	}

	report, err := h.engine.Verify(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.OK() {
		t.Fatalf("verification problems: %+v", report.Problems)
	}
}

// TestObserverSeesOrderedFacts asserts the observer contract: facts arrive in
// execution order for a plain sequential run.
func TestObserverSeesOrderedFacts(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		return ok("ok", 1, 1), nil
	})

	def := h.workflow("observed", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("one", agent("one")).
			WithPhase("two", agent("two"))
	})

	h.run(def)

	started := h.observer.Started()
	if len(started) != 2 || started[0] != "one" || started[1] != "two" {
		t.Fatalf("observer started = %v, want [one two]", started)
	}
	finished := h.observer.Finished()
	if len(finished) != 2 || finished[0].Status != domain.PhaseSucceeded {
		t.Fatalf("observer finished = %+v", finished)
	}
}

// TestEveryDeclaredOutputKindIsCommitted pins data-model.md §3 invariant 2: a
// phase that promises several output kinds cannot be recorded succeeded until it
// has committed an artifact for each of them. The kinds are content-addressed, so
// the phase's single produced document becomes one artifact per declared kind.
func TestEveryDeclaredOutputKindIsCommitted(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return ok("one document for "+req.Metadata["phase"], 5, 2), nil
	})

	def := h.workflow("multi-output", func(b *definition.Builder) *definition.Builder {
		return b.WithPhase("deliver", agent("deliver"),
			definition.WithOutputs("plan", "report"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	refs, err := h.engine.Artifacts(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}

	kinds := make(map[string]int)
	digests := make(map[string]bool)
	for _, ref := range refs {
		kinds[ref.Kind]++
		if digests[ref.SHA256] {
			t.Errorf("artifact %s was committed twice, but a phase must not duplicate a digest", ref.SHA256)
		}
		digests[ref.SHA256] = true
	}
	if kinds["plan"] != 1 {
		t.Errorf("plan artifacts = %d, want 1", kinds["plan"])
	}
	if kinds["report"] != 1 {
		t.Errorf("report artifacts = %d, want 1", kinds["report"])
	}
}
