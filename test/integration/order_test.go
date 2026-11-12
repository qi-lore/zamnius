package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/engine"
)

// TestPhasesRunInDeclaredOrderAndPublishArtifactsFirst is US1 scenario 1: a phase
// begins only after the previous one is terminal, and the previous phase's
// artifact exists before the next phase's phase.started event.
func TestPhasesRunInDeclaredOrderAndPublishArtifactsFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return ok("content for "+req.Metadata["phase"], 10, 5), nil
	})

	def := h.workflow("ordered", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("one", agent("one"), definition.WithOutputs("plan")).
			WithPhase("two", agent("two"), definition.WithOutputs("report")).
			WithPhase("three", agent("three"), definition.WithOutputs("report")).
			WithPhase("four", agent("four"), definition.WithOutputs("report"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	types := h.eventTypes(result.RunID)
	if indexOfEvent(types, string(domain.EventRunSucceeded)) < 0 {
		t.Fatalf("no run.succeeded event: %v", types)
	}

	// Every phase.started of phase N+1 must follow phase N's succeeded event.
	events := h.events(result.RunID)
	var succeededAt, nextStartedAt int
	for i, event := range events {
		if event.Type == string(domain.EventPhaseSucceeded) && event.Phase != nil && *event.Phase == "one" {
			succeededAt = i
		}
		if event.Type == string(domain.EventPhaseStarted) && event.Phase != nil && *event.Phase == "two" {
			nextStartedAt = i
		}
	}
	if succeededAt == 0 || nextStartedAt == 0 || nextStartedAt <= succeededAt {
		t.Fatalf("phase two started before phase one succeeded: succeeded@%d started@%d", succeededAt, nextStartedAt)
	}

	// The next phase's attempt must also record the earlier artifact as an input,
	// which is what makes provenance verifiable rather than decorative.
	attempts, err := h.store.Attempts(context.Background(), result.RunID, "two")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts of phase two = %d, want 1", len(attempts))
	}
	firstAttempts, err := h.store.Attempts(context.Background(), result.RunID, "one")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(firstAttempts) != 1 || len(firstAttempts[0].Artifacts) != 1 {
		t.Fatalf("phase one artifacts = %+v, want one committed artifact", firstAttempts)
	}
	if attempts[0].InputFingerprint == firstAttempts[0].InputFingerprint {
		t.Error("input fingerprints are identical; the upstream artifact must be part of the inputs")
	}

	if len(result.Phases) != 4 {
		t.Fatalf("phases in result = %d, want 4", len(result.Phases))
	}
	for i, phase := range result.Phases {
		if phase.Status != domain.PhaseSucceeded {
			t.Errorf("phase %d status = %q, want succeeded", i, phase.Status)
		}
	}
}

// TestFailedAllJoinProducesNoAggregate names the failed branch and leaves no
// partial aggregate behind (US1 scenario 5).
func TestFailedAllJoinProducesNoAggregate(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["attempt"] == "beta" {
			return domain.ModelResponse{}, fakesError(domain.ErrClassPermanent, "beta refuses")
		}
		return ok("ok "+req.Metadata["attempt"], 4, 2), nil
	})

	def := h.workflow("all-join", func(b *definition.Builder) *definition.Builder {
		return b.WithFanout("parallel",
			definition.JoinPolicy{Rule: definition.JoinAll},
			[]definition.Branch{
				definition.BranchOf("alpha", agent("alpha")),
				definition.BranchOf("beta", agent("beta")),
				definition.BranchOf("gamma", agent("gamma")),
			},
			definition.WithOutputs("report"))
	})

	result, err := h.engine.Start(context.Background(), def, engine.StartOptions{Observer: h.observer})
	if err == nil {
		t.Fatal("Start() returned no error for a failed all join")
	}
	if result.Status != domain.RunFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}

	refs, err := h.engine.Artifacts(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	for _, ref := range refs {
		if ref.Kind == "aggregate" {
			t.Fatalf("a failed all join produced an aggregate artifact: %s", ref.SHA256)
		}
	}

	phases, err := h.store.Phases(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	if phases[0].JoinResult == nil {
		t.Fatal("join result not recorded")
	}
	if phases[0].JoinResult.Satisfied {
		t.Error("join reported satisfied despite a failed branch")
	}
	if phases[0].FailureReason == nil || !strings.Contains(*phases[0].FailureReason, "beta") {
		t.Errorf("failure reason = %v, want it to name the failed branch", phases[0].FailureReason)
	}
}

// TestNofMJoinRequiresExactlyN names the boundary of the n_of_m rule.
func TestNofMJoinRequiresExactlyN(t *testing.T) {
	t.Parallel()

	n := 2
	build := func(b *definition.Builder) *definition.Builder {
		return b.WithFanout("parallel",
			definition.JoinPolicy{Rule: definition.JoinNofM, N: &n},
			[]definition.Branch{
				definition.BranchOf("alpha", agent("alpha")),
				definition.BranchOf("beta", agent("beta")),
				definition.BranchOf("gamma", agent("gamma")),
			})
	}

	t.Run("two successes satisfy", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
			if req.Metadata["attempt"] == "gamma" {
				return domain.ModelResponse{}, fakesError(domain.ErrClassPermanent, "gamma refuses")
			}
			return ok("ok", 3, 1), nil
		})

		result := h.run(h.workflow("two-of-three", build))
		if result.Status != domain.RunSucceeded {
			t.Fatalf("status = %q, want succeeded", result.Status)
		}
		phases, err := h.store.Phases(context.Background(), result.RunID)
		if err != nil {
			t.Fatalf("Phases() error = %v", err)
		}
		if phases[0].JoinResult == nil || !phases[0].JoinResult.Satisfied {
			t.Fatalf("join not satisfied: %+v", phases[0].JoinResult)
		}
	})

	t.Run("one success fails", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
			if req.Metadata["attempt"] == "alpha" {
				return ok("ok", 3, 1), nil
			}
			return domain.ModelResponse{}, fakesError(domain.ErrClassPermanent, "refuses")
		})

		result, err := h.engine.Start(context.Background(), h.workflow("one-of-three", build), engine.StartOptions{})
		if err == nil {
			t.Fatal("Start() returned no error for an unsatisfied n_of_m join")
		}
		if result.Status != domain.RunFailed {
			t.Errorf("status = %q, want failed", result.Status)
		}
	})
}

// TestAnyJoinToleratesFailures covers the third join rule and the aggregate that
// a satisfied any-join produces.
func TestAnyJoinToleratesFailures(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["attempt"] == "alpha" {
			return ok("the surviving branch", 5, 3), nil
		}
		return domain.ModelResponse{}, fakesError(domain.ErrClassPermanent, "refuses")
	})

	def := h.workflow("any-join", func(b *definition.Builder) *definition.Builder {
		return b.WithFanout("parallel",
			definition.JoinPolicy{Rule: definition.JoinAny},
			[]definition.Branch{
				definition.BranchOf("alpha", agent("alpha")),
				definition.BranchOf("beta", agent("beta")),
			},
			definition.WithOutputs("report"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	refs, err := h.engine.Artifacts(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	aggregates := 0
	for _, ref := range refs {
		if ref.Kind == "aggregate" {
			aggregates++
		}
	}
	if aggregates != 1 {
		t.Fatalf("aggregate artifacts = %d, want 1", aggregates)
	}
}

// TestWhenConditionSkipsAPhase records phase.skipped with the observed outcome.
func TestWhenConditionSkipsAPhase(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["phase"] == "first" {
			return domain.ModelResponse{}, fakesError(domain.ErrClassPermanent, "first fails on purpose")
		}
		return ok("ok", 1, 1), nil
	})

	def := h.workflow("skipping", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first"), definition.OnFailure(definition.FailContinue)).
			WithPhase("compensate", agent("compensate"),
				definition.When("first", definition.OutcomeSucceeded)).
			WithPhase("report", agent("report"))
	})

	result := h.run(def)
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	if index := indexOfEvent(h.eventTypes(result.RunID), string(domain.EventPhaseSkipped)); index < 0 {
		t.Fatalf("no phase.skipped event: %v", h.eventTypes(result.RunID))
	}
	phases, err := h.store.Phases(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	statuses := map[string]domain.PhaseStatus{}
	for _, phase := range phases {
		statuses[phase.Name] = phase.Status
	}
	if statuses["compensate"] != domain.PhaseSkipped {
		t.Errorf("compensate status = %q, want skipped", statuses["compensate"])
	}
	if statuses["first"] != domain.PhaseDegraded {
		t.Errorf("first status = %q, want degraded", statuses["first"])
	}

	decisions, err := h.store.Decisions(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	var skipDecision bool
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionSkip && strings.Contains(decision.Rationale, "succeeded") {
			skipDecision = true
		}
	}
	if !skipDecision {
		t.Errorf("no skip decision naming the required outcome: %+v", decisions)
	}
}
