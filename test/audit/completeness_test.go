package audit_test

// T056 — audit completeness (SC-005, US3 scenarios 1 and 2). Two claims are
// checked against records real runs wrote:
//
//  1. Every event is attributed: a valid type, a well-formed actor, a timestamp,
//     the phase it belongs to where the fact is phase-scoped, and a payload that
//     carries the facts explaining it. Every phase that reached a terminal state
//     has its start and its terminal transition in the log, in order.
//  2. Every retry, escalation, budget stop, takeover, skip, join, and resume
//     restart has a decision record whose rationale names the triggering
//     condition, whose evidence exposes the same condition, and whose matching
//     event is present in the log.
//
// The corpus is built from real engines and stores, because a completeness check
// over hand-built rows would only prove the test can assert itself (Principle III).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/test/fakes"
)

// TestVerifyEveryEventIsAttributed proves US3 scenario 1: every phase transition
// in a run's record names its actor, timestamp, phase, and the facts that explain
// it, and every phase that reached a terminal state has both its start and its
// terminal event in order.
func TestVerifyEveryEventIsAttributed(t *testing.T) {
	for _, sample := range auditCorpus(t) {
		sample := sample
		t.Run(sample.name, func(t *testing.T) {
			ctx := context.Background()

			phases, err := sample.f.store.Phases(ctx, sample.runID)
			if err != nil {
				t.Fatalf("Phases() error = %v", err)
			}
			declared := make(map[string]domain.PhaseState, len(phases))
			for _, phase := range phases {
				declared[phase.Name] = phase
			}

			events := sample.f.events(sample.runID)
			if len(events) == 0 {
				t.Fatal("run wrote no events")
			}

			var previousSeq int64
			for _, event := range events {
				if event.Seq <= previousSeq {
					t.Errorf("seq %d does not follow %d; events are not ordered", event.Seq, previousSeq)
				}
				previousSeq = event.Seq

				if !domain.EventType(event.Type).Valid() {
					t.Errorf("event seq %d has type %q outside the closed vocabulary", event.Seq, event.Type)
				}
				if !validActor(event.Actor) {
					t.Errorf("event seq %d (%s) has actor %q, want system, agent:<name>, or operator:<id>",
						event.Seq, event.Type, event.Actor)
				}
				if event.TS.IsZero() {
					t.Errorf("event seq %d (%s) has no timestamp", event.Seq, event.Type)
				}

				payload := payloadOf(t, event)
				if len(payload) == 0 {
					t.Errorf("event seq %d (%s) has an empty payload; it explains nothing", event.Seq, event.Type)
				}
				for _, key := range requiredCauseKeys[event.Type] {
					if _, present := payload[key]; !present {
						t.Errorf("event seq %d (%s) payload %v does not carry the cause field %q",
							event.Seq, event.Type, payload, key)
					}
				}

				if phaseScoped(event.Type) {
					if event.Phase == nil || *event.Phase == "" {
						t.Errorf("event seq %d (%s) has no phase, but the fact is phase-scoped", event.Seq, event.Type)
					}
				}
				if event.Phase != nil {
					if _, ok := declared[*event.Phase]; !ok {
						t.Errorf("event seq %d (%s) names phase %q, which the run does not declare",
							event.Seq, event.Type, *event.Phase)
					}
				}
				if strings.HasPrefix(event.Type, "attempt.") && event.AttemptID == nil {
					t.Errorf("event seq %d (%s) has no attempt id", event.Seq, event.Type)
				}
			}

			checkPhaseLifecycle(t, sample, events, phases)
		})
	}
}

// TestVerifyEveryDecisionNamesItsTrigger proves US3 scenario 2 and SC-005: each
// non-trivial choice in the corpus has a decision record that names the condition
// that triggered it, exposes that condition in its evidence, and is backed by the
// event the choice was made about.
func TestVerifyEveryDecisionNamesItsTrigger(t *testing.T) {
	seenKinds := make(map[domain.DecisionKind]bool)
	seenPolicies := make(map[string]bool)

	for _, sample := range auditCorpus(t) {
		sample := sample
		t.Run(sample.name, func(t *testing.T) {
			events := sample.f.events(sample.runID)
			decisions := sample.f.decisions(sample.runID)
			if len(decisions) == 0 {
				t.Fatalf("run %s recorded no decisions", sample.runID)
			}

			for _, decision := range decisions {
				seenKinds[decision.Kind] = true
				checkDecision(t, sample.name, decision, events)
				if decision.Kind == domain.DecisionFailurePolicy {
					seenPolicies[stringField(t, evidenceOf(t, decision), "policy")] = true
				}
			}

			// A decision.recorded event is the event form of a decision: it must
			// resolve to a record of the same kind, or the timeline would announce a
			// choice the record cannot explain.
			for _, event := range events {
				if event.Type != string(domain.EventDecisionRecorded) {
					continue
				}
				kind := domain.DecisionKind(stringField(t, payloadOf(t, event), "kind"))
				found := false
				for _, decision := range decisions {
					if decision.Kind == kind {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("event seq %d announces decision %q, but no such decision record exists", event.Seq, kind)
				}
			}
		})
	}

	for _, kind := range []domain.DecisionKind{
		domain.DecisionRetry, domain.DecisionJoin, domain.DecisionFailurePolicy,
		domain.DecisionBudgetStop, domain.DecisionTakeover, domain.DecisionSkip,
		domain.DecisionResumeRestart,
	} {
		if !seenKinds[kind] {
			t.Errorf("the corpus produced no %s decision; the completeness check would not cover it", kind)
		}
	}
	for _, policy := range []string{string(definition.FailContinue), string(definition.FailEscalate)} {
		if !seenPolicies[policy] {
			t.Errorf("the corpus exercised no failure_policy %q decision", policy)
		}
	}
}

// completenessCase is one run in the audit corpus.
type completenessCase struct {
	name  string
	f     *auditFixture
	runID uuid.UUID
}

// auditCorpus builds one run per decision kind plus a clean run. Every run is
// driven through a real engine and store; only the model is scripted.
func auditCorpus(t *testing.T) []completenessCase {
	t.Helper()

	return []completenessCase{
		cleanCase(t),
		retryCase(t),
		escalationCase(t),
		budgetCase(t),
		skipCase(t),
		takeoverCase(t),
		resumeRestartCase(t),
		driftOverrideCase(t),
	}
}

// cleanCase is the baseline: ordered phases plus a fan-out joined with `all`, so
// the log has ordinary phase transitions and a join decision.
func cleanCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	return completenessCase{name: "clean", f: f, runID: run.RunID}
}

// retryCase fails the first attempt transiently, so the phase succeeds on its
// second attempt and a retry decision has to explain why.
func retryCase(t *testing.T) completenessCase {
	t.Helper()

	var calls atomic.Int32
	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		if calls.Add(1) == 1 {
			return domain.ModelResponse{}, transientFailure("first attempt fails on purpose")
		}
		return okResponse("recovered"), nil
	}))

	def := f.workflow("retry-completeness", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("flaky", agent("flaky"), definition.WithRetry(definition.Retry{MaxAttempts: 2})).
			WithPhase("after", agent("after"))
	})
	run := f.record(def)
	return completenessCase{name: "retry", f: f, runID: run.RunID}
}

// escalationCase exhausts the retry allowance, so the failure policy escalates and
// the run stops with a decision that names the class and the policy.
func escalationCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{}, transientFailure("provider never recovers in this scenario")
	}))

	def := f.workflow("escalation-completeness", func(b *definition.Builder) *definition.Builder {
		return b.WithPhase("doomed", agent("doomed"),
			definition.WithRetry(definition.Retry{MaxAttempts: 2}),
			definition.OnFailure(definition.FailEscalate))
	})
	result, err := f.engine.Start(context.Background(), def, engine.StartOptions{})
	if err == nil {
		t.Fatal("Start() returned no error for an escalated phase")
	}
	if result.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", result.Status)
	}
	return completenessCase{name: "escalation", f: f, runID: result.RunID}
}

// budgetCase spends the whole token allowance in the first phase, so the second
// phase stops before calling the model and records the exhausted unit.
func budgetCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{
			Content:    "spendy",
			Usage:      domain.Usage{InputTokens: 6, OutputTokens: 4},
			StopReason: domain.StopEnd,
		}, nil
	}))

	tokens := 10
	def := f.workflow("budget-completeness", func(b *definition.Builder) *definition.Builder {
		return b.
			WithBudget(domain.Budget{Tokens: &tokens}).
			WithPhase("expensive", agent("expensive")).
			WithPhase("second", agent("second"), definition.OnFailure(definition.FailContinue))
	})
	run := f.record(def)
	return completenessCase{name: "budget", f: f, runID: run.RunID}
}

// skipCase records a when-condition that did not hold, so the skipped phase has a
// decision naming the required and observed outcomes.
func skipCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixtureWithModel(t, fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		if req.Metadata["phase"] == "first" {
			return domain.ModelResponse{}, permanentFailure("first phase fails on purpose")
		}
		return okResponse("output of " + req.Metadata["attempt"]), nil
	}))

	def := f.workflow("skip-completeness", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("first", agent("first"), definition.OnFailure(definition.FailContinue)).
			WithPhase("compensate", agent("compensate"), definition.When("first", definition.OutcomeSucceeded)).
			WithPhase("report", agent("report"))
	})
	run := f.record(def)
	return completenessCase{name: "skip", f: f, runID: run.RunID}
}

// takeoverCase leaves an expired lease behind and takes it over, so the record
// explains why two owners advanced one run.
func takeoverCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()
	ctx := context.Background()

	if _, err := f.store.AcquireLease(ctx, run.RunID, "audit-first", 20*time.Millisecond); err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := f.store.AcquireLease(ctx, run.RunID, "audit-second", time.Second); err != nil {
		t.Fatalf("takeover AcquireLease() error = %v", err)
	}
	return completenessCase{name: "takeover", f: f, runID: run.RunID}
}

// resumeRestartCase leaves the durable state a crashed process leaves — a running
// attempt with no committed outcome — and resumes, so reconciliation records an
// abandoned attempt and the restart decision explaining it.
func resumeRestartCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()
	f.simulateRunningAttempt(run.RunID, "review", 1, "review")

	resumed, err := f.engine.Resume(context.Background(), run.RunID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}
	return completenessCase{name: "resume_restart", f: f, runID: run.RunID}
}

// driftOverrideCase resumes with a supplied definition that no longer matches the
// run's fingerprint, accepted by explicit override. The operator's choice is a
// decision.recorded event backed by a resume_restart decision naming both
// fingerprints.
func driftOverrideCase(t *testing.T) completenessCase {
	t.Helper()

	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()

	// Any difference changes the fingerprint; the engine still executes the
	// recorded snapshot, so only the intent check sees this definition.
	drifted := f.workflow("artifact-integrity", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("plan", definition.Agent("plan", "a different instruction after the fact", "gpt-4.1-mini", 256),
				definition.WithOutputs("plan")).
			WithPhase("review", agent("review"), definition.WithOutputs("report"))
	})

	resumed, err := f.engine.Resume(context.Background(), run.RunID, engine.ResumeOptions{
		Definition:           &drifted,
		AllowDefinitionDrift: true,
	})
	if err != nil {
		t.Fatalf("Resume() with an allowed drift error = %v", err)
	}
	if resumed.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", resumed.Status)
	}
	return completenessCase{name: "drift_override", f: f, runID: run.RunID}
}

// checkPhaseLifecycle asserts every phase that reached a terminal state has its
// start (unless skipped) and exactly one terminal event, in order, with a payload
// naming the recorded outcome.
func checkPhaseLifecycle(t *testing.T, sample completenessCase, events []domain.Event, phases []domain.PhaseState) {
	t.Helper()

	for _, phase := range phases {
		var terminals []domain.Event
		var starts []domain.Event
		for _, event := range events {
			if event.Phase == nil || *event.Phase != phase.Name {
				continue
			}
			switch event.Type {
			case string(domain.EventPhaseStarted):
				starts = append(starts, event)
			case string(domain.EventPhaseSucceeded), string(domain.EventPhaseFailed),
				string(domain.EventPhaseDegraded), string(domain.EventPhaseSkipped):
				terminals = append(terminals, event)
			}
		}

		wantTerminal := terminalEventFor(phase.Status)
		if wantTerminal == "" {
			if len(terminals) != 0 {
				t.Errorf("%s: phase %q is %s but has %d terminal event(s): %v",
					sample.name, phase.Name, phase.Status, len(terminals), typesOf(terminals))
			}
			continue
		}
		if len(terminals) != 1 {
			t.Fatalf("%s: phase %q is %s but has %d terminal events, want exactly 1: %v",
				sample.name, phase.Name, phase.Status, len(terminals), typesOf(terminals))
		}
		terminal := terminals[0]
		if terminal.Type != wantTerminal {
			t.Errorf("%s: phase %q is %s but its terminal event is %s, want %s",
				sample.name, phase.Name, phase.Status, terminal.Type, wantTerminal)
		}

		if phase.Status != domain.PhaseSkipped {
			if len(starts) == 0 {
				t.Errorf("%s: phase %q executed but has no phase.started event", sample.name, phase.Name)
			} else if lastStart := starts[len(starts)-1]; lastStart.Seq >= terminal.Seq {
				t.Errorf("%s: phase %q terminal event at seq %d precedes its last start at seq %d",
					sample.name, phase.Name, terminal.Seq, lastStart.Seq)
			}
		}

		payload := payloadOf(t, terminal)
		if phase.Status != domain.PhaseSkipped {
			if got := stringField(t, payload, "status"); got != string(phase.Status) {
				t.Errorf("%s: phase %q terminal payload status = %q, recorded status = %q",
					sample.name, phase.Name, got, phase.Status)
			}
		} else if stringField(t, payload, "observed") == "" {
			t.Errorf("%s: skipped phase %q payload %v does not name the observed outcome",
				sample.name, phase.Name, payload)
		}
	}
}

// checkDecision asserts one decision names its triggering condition, exposes it in
// the evidence, and has the event it was made about.
func checkDecision(t *testing.T, sample string, decision domain.Decision, events []domain.Event) {
	t.Helper()

	if decision.TS.IsZero() {
		t.Errorf("%s: %s decision has no timestamp", sample, decision.Kind)
	}
	if strings.TrimSpace(decision.Summary) == "" {
		t.Errorf("%s: %s decision has no summary", sample, decision.Kind)
	}
	if strings.TrimSpace(decision.Rationale) == "" {
		t.Errorf("%s: %s decision has no rationale", sample, decision.Kind)
	}

	evidence := evidenceOf(t, decision)
	if len(evidence) == 0 {
		t.Errorf("%s: %s decision has empty evidence", sample, decision.Kind)
	}

	switch decision.Kind {
	case domain.DecisionRetry:
		requirePhase(t, sample, decision)
		class := stringField(t, evidence, "error_class")
		if class == "" {
			t.Errorf("%s: retry decision evidence %v does not name the error class", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, class) || !strings.Contains(decision.Rationale, "retry_on") {
			t.Errorf("%s: retry rationale %q does not name the condition (class %q listed in retry_on)",
				sample, decision.Rationale, class)
		}
		failed := findEvent(events, func(event domain.Event) bool {
			return event.Type == string(domain.EventAttemptFailed) && sameAttempt(event, decision)
		})
		if failed == nil {
			t.Errorf("%s: retry decision names no attempt.failed event", sample)
		} else if got := stringField(t, payloadOf(t, *failed), "error_class"); got != class {
			t.Errorf("%s: retry evidence class %q disagrees with the failed attempt's %q", sample, class, got)
		}

	case domain.DecisionFailurePolicy:
		requirePhase(t, sample, decision)
		policy := stringField(t, evidence, "policy")
		if policy == "" {
			t.Errorf("%s: failure_policy evidence %v does not name the policy", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, policy) {
			t.Errorf("%s: failure_policy rationale %q does not name policy %q", sample, decision.Rationale, policy)
		}
		if findEvent(events, func(event domain.Event) bool {
			return event.Phase != nil && decision.Phase != nil && *event.Phase == *decision.Phase &&
				(event.Type == string(domain.EventPhaseFailed) || event.Type == string(domain.EventPhaseDegraded))
		}) == nil {
			t.Errorf("%s: failure_policy for %q has no failed or degraded phase event", sample, *decision.Phase)
		}

	case domain.DecisionBudgetStop:
		requirePhase(t, sample, decision)
		unit := stringField(t, evidence, "unit")
		if unit == "" {
			t.Errorf("%s: budget_stop evidence %v does not name the exhausted unit", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, unit) {
			t.Errorf("%s: budget_stop rationale %q does not name unit %q", sample, decision.Rationale, unit)
		}
		if findEvent(events, func(event domain.Event) bool {
			return event.Type == string(domain.EventPhaseBudgetExhausted) && event.Phase != nil &&
				decision.Phase != nil && *event.Phase == *decision.Phase
		}) == nil {
			t.Errorf("%s: budget_stop for %q has no phase.budget_exhausted event", sample, *decision.Phase)
		}

	case domain.DecisionTakeover:
		previous := stringField(t, evidence, "previous_owner")
		next := stringField(t, evidence, "new_owner")
		if previous == "" || next == "" {
			t.Errorf("%s: takeover evidence %v does not name both owners", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, "expired") {
			t.Errorf("%s: takeover rationale %q does not name the expired lease", sample, decision.Rationale)
		}
		takeover := findEvent(events, func(event domain.Event) bool {
			return event.Type == string(domain.EventRunTakenOver)
		})
		if takeover == nil {
			t.Errorf("%s: takeover decision has no run.taken_over event", sample)
		} else if got := stringField(t, payloadOf(t, *takeover), "previous_owner"); got != previous {
			t.Errorf("%s: takeover event names previous owner %q, decision evidence %q", sample, got, previous)
		}

	case domain.DecisionSkip:
		requirePhase(t, sample, decision)
		afterPhase := stringField(t, evidence, "after_phase")
		observed := stringField(t, evidence, "observed")
		if afterPhase == "" || observed == "" {
			t.Errorf("%s: skip evidence %v does not name the condition and its outcome", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, afterPhase) || !strings.Contains(decision.Rationale, observed) {
			t.Errorf("%s: skip rationale %q does not name phase %q observed as %q",
				sample, decision.Rationale, afterPhase, observed)
		}
		if findEvent(events, func(event domain.Event) bool {
			return event.Type == string(domain.EventPhaseSkipped) && event.Phase != nil &&
				decision.Phase != nil && *event.Phase == *decision.Phase
		}) == nil {
			t.Errorf("%s: skip decision for %q has no phase.skipped event", sample, *decision.Phase)
		}

	case domain.DecisionJoin:
		requirePhase(t, sample, decision)
		rule := stringField(t, evidence, "rule")
		if rule == "" {
			t.Errorf("%s: join evidence %v does not name the join rule", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, rule) {
			t.Errorf("%s: join rationale %q does not name rule %q", sample, decision.Rationale, rule)
		}
		if findEvent(events, func(event domain.Event) bool {
			return event.Phase != nil && decision.Phase != nil && *event.Phase == *decision.Phase &&
				event.Type == string(domain.EventPhaseSucceeded) &&
				payloadOf(t, event)["join_satisfied"] == true
		}) == nil {
			t.Errorf("%s: join decision for %q has no succeeded phase event with a join result", sample, *decision.Phase)
		}

	case domain.DecisionResumeRestart:
		if decision.AttemptID != nil {
			if !strings.Contains(decision.Rationale, "no committed outcome") {
				t.Errorf("%s: restart rationale %q does not name the missing outcome", sample, decision.Rationale)
			}
			abandoned := findEvent(events, func(event domain.Event) bool {
				return event.Type == string(domain.EventAttemptAbandoned) && sameAttempt(event, decision)
			})
			if abandoned == nil {
				t.Errorf("%s: restart decision names no abandoned attempt event", sample)
			}
			return
		}
		expected := stringField(t, evidence, "expected_fingerprint")
		supplied := stringField(t, evidence, "supplied_fingerprint")
		if expected == "" || supplied == "" {
			t.Errorf("%s: drift override evidence %v does not name both fingerprints", sample, evidence)
		}
		if !strings.Contains(decision.Rationale, expected) || !strings.Contains(decision.Rationale, supplied) {
			t.Errorf("%s: drift rationale %q does not name expected %q and supplied %q fingerprints",
				sample, decision.Rationale, expected, supplied)
		}
		if findEvent(events, func(event domain.Event) bool {
			return event.Type == string(domain.EventDecisionRecorded) &&
				stringField(t, payloadOf(t, event), "kind") == string(domain.DecisionResumeRestart)
		}) == nil {
			t.Errorf("%s: drift override has no decision.recorded event", sample)
		}

	default:
		t.Errorf("%s: %q is not a decision kind this check knows how to audit", sample, decision.Kind)
	}
}

// simulateRunningAttempt writes the durable state a process killed mid-attempt
// leaves: a running phase and a running attempt with no outcome (ADR-0003).
func (f *auditFixture) simulateRunningAttempt(runID uuid.UUID, phase string, attemptNo int, agentName string) {
	f.t.Helper()
	ctx := context.Background()

	phases, err := f.store.Phases(ctx, runID)
	if err != nil {
		f.t.Fatalf("Phases() error = %v", err)
	}
	var state domain.PhaseState
	for _, candidate := range phases {
		if candidate.Name == phase {
			state = candidate
			break
		}
	}
	if state.Name == "" {
		f.t.Fatalf("run %s declares no phase %q", runID, phase)
	}

	now := time.Now().UTC()
	state.Status = domain.PhaseRunning
	if state.StartedAt == nil {
		state.StartedAt = &now
	}
	if err := f.store.CommitPhaseResult(ctx, domain.PhaseCommit{
		Phase: state,
		Events: []domain.EventDraft{{
			Type:    domain.EventPhaseStarted,
			Actor:   domain.ActorSystem,
			Phase:   phase,
			Payload: map[string]any{"ordinal": state.Ordinal},
		}},
	}); err != nil {
		f.t.Fatalf("commit crashed phase %q: %v", phase, err)
	}

	attempt := domain.Attempt{
		ID:               uuid.New(),
		RunID:            runID,
		Phase:            phase,
		AttemptNo:        attemptNo,
		Status:           domain.AttemptRunning,
		AgentName:        agentName,
		ModelProvider:    "openai-compatible",
		ModelName:        "gpt-4.1-mini",
		InputFingerprint: "crashed-before-the-outcome",
		PromptVersion:    "crashed-before-the-outcome",
		StartedAt:        now,
	}
	if err := f.store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt: attempt,
		Events: []domain.EventDraft{{
			Type:      domain.EventAttemptStarted,
			Actor:     domain.ActorAgent(agentName),
			Phase:     phase,
			AttemptID: attempt.ID,
			Payload: map[string]any{
				"attempt_no":        attemptNo,
				"branch":            phase,
				"agent":             agentName,
				"model_provider":    attempt.ModelProvider,
				"model_name":        attempt.ModelName,
				"prompt_version":    attempt.PromptVersion,
				"input_fingerprint": attempt.InputFingerprint,
			},
		}},
	}); err != nil {
		f.t.Fatalf("commit crashed attempt of %q: %v", phase, err)
	}
}

// requiredCauseKeys names the payload fact that explains each event type. An event
// without it would record that something happened but not what caused it.
var requiredCauseKeys = map[string][]string{
	string(domain.EventRunCreated):            {"definition_fingerprint"},
	string(domain.EventRunStarted):            {"definition_fingerprint"},
	string(domain.EventRunPaused):             {"reason"},
	string(domain.EventRunResumed):            {"reason"},
	string(domain.EventRunTakenOver):          {"previous_owner"},
	string(domain.EventRunCancelRequested):    {"reason"},
	string(domain.EventRunSucceeded):          {"reason"},
	string(domain.EventRunFailed):             {"reason"},
	string(domain.EventRunCancelled):          {"reason"},
	string(domain.EventPhaseStarted):          {"ordinal"},
	string(domain.EventPhaseSucceeded):        {"status"},
	string(domain.EventPhaseFailed):           {"status"},
	string(domain.EventPhaseDegraded):         {"status"},
	string(domain.EventPhaseSkipped):          {"observed"},
	string(domain.EventPhaseBudgetExhausted):  {"unit"},
	string(domain.EventAttemptStarted):        {"attempt_no"},
	string(domain.EventAttemptSucceeded):      {"attempt_no"},
	string(domain.EventAttemptFailed):         {"error_class"},
	string(domain.EventAttemptAbandoned):      {"attempt_no"},
	string(domain.EventArtifactCommitted):     {"sha256"},
	string(domain.EventArtifactVerified):      {"sha256"},
	string(domain.EventDecisionRecorded):      {"kind"},
	string(domain.EventStateMigrated):         {"from_schema_version"},
	string(domain.EventStateMigrationRefused): {"run_schema_version"},
}

// phaseScoped reports whether an event type describes a fact about one phase.
func phaseScoped(eventType string) bool {
	return strings.HasPrefix(eventType, "phase.") ||
		strings.HasPrefix(eventType, "attempt.") ||
		strings.HasPrefix(eventType, "artifact.")
}

// validActor accepts the three documented actor forms.
func validActor(actor string) bool {
	if actor == domain.ActorSystem {
		return true
	}
	for _, prefix := range []string{"agent:", "operator:"} {
		if strings.HasPrefix(actor, prefix) && len(actor) > len(prefix) {
			return true
		}
	}
	return false
}

// terminalEventFor maps a phase status onto the event that records it, or "" when
// the status is not terminal.
func terminalEventFor(status domain.PhaseStatus) string {
	switch status {
	case domain.PhaseSucceeded:
		return string(domain.EventPhaseSucceeded)
	case domain.PhaseFailed:
		return string(domain.EventPhaseFailed)
	case domain.PhaseDegraded:
		return string(domain.EventPhaseDegraded)
	case domain.PhaseSkipped:
		return string(domain.EventPhaseSkipped)
	default:
		return ""
	}
}

// requirePhase asserts a decision about a phase names one.
func requirePhase(t *testing.T, sample string, decision domain.Decision) {
	t.Helper()
	if decision.Phase == nil || *decision.Phase == "" {
		t.Errorf("%s: %s decision does not name the phase it was made about", sample, decision.Kind)
	}
}

// sameAttempt reports whether an event belongs to the attempt a decision names.
func sameAttempt(event domain.Event, decision domain.Decision) bool {
	return event.AttemptID != nil && decision.AttemptID != nil &&
		*event.AttemptID == *decision.AttemptID
}

// findEvent returns the first event matching match.
func findEvent(events []domain.Event, match func(domain.Event) bool) *domain.Event {
	for i := range events {
		if match(events[i]) {
			return &events[i]
		}
	}
	return nil
}

// payloadOf decodes an event payload into the fact map the writer stored.
func payloadOf(t *testing.T, event domain.Event) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("event seq %d payload is not a JSON object: %v", event.Seq, err)
	}
	return payload
}

// evidenceOf decodes a decision's evidence into the pointers the writer stored.
func evidenceOf(t *testing.T, decision domain.Decision) map[string]any {
	t.Helper()

	var evidence map[string]any
	if err := json.Unmarshal(decision.Evidence, &evidence); err != nil {
		t.Fatalf("%s decision evidence is not a JSON object: %v", decision.Kind, err)
	}
	return evidence
}

// stringField renders one fact as a string, tolerating the JSON number type.
func stringField(t *testing.T, object map[string]any, key string) string {
	t.Helper()

	value, ok := object[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// typesOf lists the types of some events, for failure messages.
func typesOf(events []domain.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

// okResponse is a successful scripted answer.
func okResponse(content string) domain.ModelResponse {
	return domain.ModelResponse{
		Content:    content,
		Usage:      domain.Usage{InputTokens: 7, OutputTokens: 3},
		StopReason: domain.StopEnd,
	}
}

// transientFailure is a retryable provider error.
func transientFailure(message string) error {
	return &domain.Error{Class: domain.ErrClassTransient, Msg: message}
}

// permanentFailure is a non-retryable provider error.
func permanentFailure(message string) error {
	return &domain.Error{Class: domain.ErrClassPermanent, Msg: message}
}
