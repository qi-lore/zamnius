package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// DefaultArtifactKind labels the artifact of a phase that declares no outputs: the
// document exists, so it is recorded under a neutral kind rather than dropped.
const DefaultArtifactKind = "report"

// phaseOutcome is the committed result of running one phase, with everything the
// caller needs to apply the failure policy or build the join.
type phaseOutcome struct {
	Status        domain.PhaseStatus
	Attempts      int
	Artifacts     []domain.ArtifactRef
	ErrorClass    domain.ErrorClass
	FailureReason string
	// BudgetUnit names the allowance that ran out, when the phase stopped for a
	// budget rather than a model failure.
	BudgetUnit string
	Join       *domain.JoinResult
	// Cause is the error that produced the outcome. It is carried so the failure
	// policy can keep the operator-facing hint (a hint naming the offending
	// provider or setting is worth more than a generic one) and so a caller can
	// still match the root error with errors.Is.
	Cause error
}

// runPhase dispatches on the declared mode. The two modes share the attempt
// lifecycle but differ in how many attempts may exist and how they are joined.
func (s *session) runPhase(ctx context.Context, index int, phase definition.Phase) (phaseOutcome, error) {
	switch phase.Mode {
	case definition.ModeSingle:
		return s.runSingle(ctx, index, phase)
	case definition.ModeFanout:
		return s.runFanout(ctx, index, phase)
	default:
		// Normalize rejects unknown modes, so this is unreachable in practice; it
		// stays because a silent default would execute the wrong shape.
		return phaseOutcome{}, fmt.Errorf("phase %q has unsupported mode %q", phase.Name, phase.Mode)
	}
}

// runSingle executes a single-agent phase: start it, run attempts until one
// succeeds or the retry policy stops, then commit the phase result.
func (s *session) runSingle(ctx context.Context, index int, phase definition.Phase) (phaseOutcome, error) {
	state, err := s.phaseState(ctx, phase.Name)
	if err != nil {
		return phaseOutcome{}, err
	}

	// A resumed run whose attempt already succeeded finishes the phase from its
	// committed result instead of calling the model again: a completed step is
	// never re-executed (FR-013, SC-002). The attempt outcome is already durable, so
	// this adds no attempt row and no spend.
	if recovered, ok := s.recoveredBranch(phase.Name, phase.Agent.Name); ok {
		outcome := phaseOutcome{
			Status:    domain.PhaseSucceeded,
			Attempts:  recovered.attemptNo,
			Artifacts: recovered.artifacts,
		}
		return outcome, s.finishPhase(ctx, phase, &state, outcome, "")
	}

	if err := s.startPhase(ctx, &state, phase.Name); err != nil {
		return phaseOutcome{}, err
	}

	outcome := s.executeAttempts(ctx, index, phase, *phase.Agent, phase.Timeout, phase.Budget, "")
	if outcome.Status == domain.PhaseCancelled {
		return outcome, nil
	}
	// The declared policy decides how a failure is recorded, and the phase row must
	// say so: `degraded` is the state that tells a reader the run continued
	// deliberately (data-model.md §3).
	if outcome.Status == domain.PhaseFailed && phase.OnFailure == definition.FailContinue {
		outcome.Status = domain.PhaseDegraded
	}

	return outcome, s.finishPhase(ctx, phase, &state, outcome, "")
}

// startPhase commits the running transition. Doing this before the first model
// call is what makes an interrupted phase visible on resume.
func (s *session) startPhase(ctx context.Context, state *domain.PhaseState, name string) error {
	started := s.engine.now()
	state.Status = domain.PhaseRunning
	if state.StartedAt == nil {
		state.StartedAt = &started
	}
	position := name
	return s.commitPhase(ctx, *state,
		&domain.RunDelta{Status: domain.RunRunning, CurrentPhase: &position},
		[]domain.EventDraft{{
			Type:    domain.EventPhaseStarted,
			Actor:   domain.ActorSystem,
			Phase:   name,
			Payload: map[string]any{"ordinal": state.Ordinal},
		}}, nil)
}

// executeAttempts runs the attempt loop for one agent. It returns a phase-level
// outcome; a branch passes its name as label so events name the branch.
func (s *session) executeAttempts(ctx context.Context, index int, phase definition.Phase, agent definition.AgentRef, timeout *domain.Duration, budget *domain.Budget, label string) phaseOutcome {
	upstream, err := s.upstreamDigests(ctx, phase)
	if err != nil {
		return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(err)}
	}
	fingerprint, err := definition.InputFingerprint(s.def, index, upstream)
	if err != nil {
		// An impossible index is a programming error, and guessing would record a
		// provenance value that means nothing.
		return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(err)}
	}
	promptVersion := definition.PromptVersion(agent.Instructions)

	attempts := 0
	for {
		if stop, reason := s.budgetStop(budget); stop {
			return s.stopForBudget(attempts, reason)
		}

		attemptNo := s.nextAttemptNo(phase.Name)
		attempts++
		attempt := domain.Attempt{
			ID:               s.engine.ids(),
			RunID:            s.run.ID,
			Phase:            phase.Name,
			AttemptNo:        attemptNo,
			Status:           domain.AttemptRunning,
			AgentName:        agent.Name,
			ModelProvider:    agent.Model.Provider,
			ModelName:        agent.Model.Name,
			InputFingerprint: fingerprint,
			PromptVersion:    promptVersion,
			StartedAt:        s.engine.now(),
		}
		// The attempt boundary is durable: a start is recorded even if the caller is
		// cancelled while the provider is being called, so the next session can see
		// exactly which attempt was in flight (ADR-0003).
		startCtx, cancelStart := durableContext(ctx)
		startErr := s.commitAttempt(startCtx, attempt, nil, []domain.EventDraft{{
			Type:      domain.EventAttemptStarted,
			Actor:     domain.ActorAgent(agent.Name),
			Phase:     phase.Name,
			AttemptID: attempt.ID,
			Payload: map[string]any{
				"attempt_no":        attemptNo,
				"branch":            labelOrPhase(label, phase.Name),
				"agent":             agent.Name,
				"model_provider":    agent.Model.Provider,
				"model_name":        agent.Model.Name,
				"prompt_version":    promptVersion,
				"input_fingerprint": fingerprint,
			},
		}}, nil)
		cancelStart()
		if startErr != nil {
			return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(startErr), Cause: startErr}
		}

		callCtx, cancel := s.attemptContext(ctx, timeout)
		response, callErr := s.callModel(callCtx, agent, phase, label)
		cancel()

		ended := s.engine.now()
		elapsed := ended.Sub(attempt.StartedAt).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		attempt.EndedAt = &ended
		attempt.Usage = domain.Consumption{Attempts: 1, WallClockMS: elapsed}
		if callErr == nil {
			attempt.Usage.TokensIn = response.Usage.InputTokens
			attempt.Usage.TokensOut = response.Usage.OutputTokens
		}

		if callErr == nil {
			refs, artifactErr := s.persistArtifacts(ctx, phase, attempt.ID, response.Content, label)
			if artifactErr != nil {
				callErr = artifactErr
			} else {
				attempt.Status = domain.AttemptSucceeded
				events := []domain.EventDraft{{
					Type:      domain.EventAttemptSucceeded,
					Actor:     domain.ActorAgent(agent.Name),
					Phase:     phase.Name,
					AttemptID: attempt.ID,
					Payload: map[string]any{
						"attempt_no":  attemptNo,
						"branch":      labelOrPhase(label, phase.Name),
						"tokens_in":   response.Usage.InputTokens,
						"tokens_out":  response.Usage.OutputTokens,
						"duration_ms": elapsed,
						"stop_reason": string(response.StopReason),
					},
				}}
				for _, ref := range refs {
					events = append(events, domain.EventDraft{
						Type:      domain.EventArtifactCommitted,
						Actor:     domain.ActorAgent(agent.Name),
						Phase:     phase.Name,
						AttemptID: attempt.ID,
						Payload: map[string]any{
							"sha256": ref.SHA256,
							"kind":   ref.Kind,
							"size":   ref.SizeBytes,
						},
					})
				}
				if err := s.commitAttempt(ctx, attempt, refs, events, nil); err != nil {
					return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(err), Cause: err}
				}
				s.addSpend(attempt.Usage)
				s.recordArtifacts(phase.Name, refs)

				if s.observer != nil {
					s.observer.OnAttemptFinished(ctx, s.run.ID, phase.Name, attemptNo, "")
				}
				return phaseOutcome{
					Status:    domain.PhaseSucceeded,
					Attempts:  attempts,
					Artifacts: refs,
				}
			}
		}

		class := errorClassOf(callErr)
		if class == "" {
			class = domain.ErrClassInternal
		}
		detail := causeOf(callErr)

		// A context stop is not a provider failure: the attempt was interrupted and
		// its real outcome is unknown, so it is recorded abandoned rather than
		// failed. The commit is detached from the cancellation so the boundary is
		// durable even though the caller has already gone away (FR-008, ADR-0003).
		if isContextStop(callErr) {
			attempt.Status = domain.AttemptAbandoned
			attempt.ErrorClass = domain.ErrClassCancelled
			attempt.ErrorDetail = &detail
			commitCtx, cancelCommit := durableContext(ctx)
			commitErr := s.commitAttempt(commitCtx, attempt, nil, []domain.EventDraft{{
				Type:      domain.EventAttemptAbandoned,
				Actor:     domain.ActorAgent(agent.Name),
				Phase:     phase.Name,
				AttemptID: attempt.ID,
				Payload: map[string]any{
					"attempt_no":  attemptNo,
					"branch":      labelOrPhase(label, phase.Name),
					"error_class": string(domain.ErrClassCancelled),
					"detail":      detail,
					"duration_ms": elapsed,
				},
			}}, nil)
			cancelCommit()
			if commitErr != nil {
				return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(commitErr), Cause: commitErr}
			}
			s.addSpend(attempt.Usage)
			if s.observer != nil {
				s.observer.OnAttemptFinished(ctx, s.run.ID, phase.Name, attemptNo, domain.ErrClassCancelled)
			}
			return phaseOutcome{Status: domain.PhaseCancelled, Attempts: attempts, ErrorClass: domain.ErrClassCancelled, FailureReason: detail, Cause: callErr}
		}

		attempt.Status = domain.AttemptFailed
		attempt.ErrorClass = class
		attempt.ErrorDetail = &detail

		retry, rationale := s.retryDecision(phase, attemptNo, class)
		decisions := make([]domain.DecisionDraft, 0, 1)
		if retry {
			decisions = append(decisions, domain.DecisionDraft{
				Phase:     phase.Name,
				AttemptID: attempt.ID,
				Kind:      domain.DecisionRetry,
				Summary:   fmt.Sprintf("retrying %s after attempt %d", phase.Name, attemptNo),
				Rationale: rationale,
				Evidence: map[string]any{
					"attempt_no":   attemptNo,
					"error_class":  string(class),
					"max_attempts": phase.Retry.MaxAttempts,
					"branch":       labelOrPhase(label, phase.Name),
				},
			})
		}
		if err := s.commitAttempt(ctx, attempt, nil, []domain.EventDraft{{
			Type:      domain.EventAttemptFailed,
			Actor:     domain.ActorAgent(agent.Name),
			Phase:     phase.Name,
			AttemptID: attempt.ID,
			Payload: map[string]any{
				"attempt_no":  attemptNo,
				"branch":      labelOrPhase(label, phase.Name),
				"error_class": string(class),
				"detail":      detail,
				"duration_ms": elapsed,
			},
		}}, decisions); err != nil {
			return phaseOutcome{Status: domain.PhaseFailed, ErrorClass: domain.ErrClassInternal, FailureReason: causeOf(err), Cause: err}
		}
		s.addSpend(attempt.Usage)
		if s.observer != nil {
			s.observer.OnAttemptFinished(ctx, s.run.ID, phase.Name, attemptNo, class)
		}

		if !retry {
			return phaseOutcome{Status: domain.PhaseFailed, Attempts: attempts, ErrorClass: class, FailureReason: detail, Cause: callErr}
		}

		if wait := s.backoffFor(phase, attemptNo+1); wait > 0 {
			if err := s.engine.sleep(ctx, wait); err != nil {
				return phaseOutcome{Status: domain.PhaseCancelled, Attempts: attempts, ErrorClass: domain.ErrClassCancelled, FailureReason: causeOf(err), Cause: err}
			}
		}
	}
}

// finishPhase commits the terminal phase row, its events, the run spend delta and,
// for a fan-out phase, the join result.
func (s *session) finishPhase(ctx context.Context, phase definition.Phase, state *domain.PhaseState, outcome phaseOutcome, label string) error {
	// A phase may not be recorded succeeded while it promises an output kind it
	// never committed (data-model.md §3 invariant 2). Every declared kind is
	// persisted with its attempt, so this is a guard against a future writer
	// breaking that promise rather than a path a run reaches today.
	if outcome.Status == domain.PhaseSucceeded {
		if missing := s.missingOutputs(phase); len(missing) > 0 {
			outcome.Status = domain.PhaseFailed
			outcome.ErrorClass = domain.ErrClassInternal
			outcome.FailureReason = fmt.Sprintf("phase %q promised output kind(s) %s but committed no such artifact",
				phase.Name, strings.Join(missing, ", "))
		}
	}

	ended := s.engine.now()
	state.Status = outcome.Status
	state.EndedAt = &ended
	state.AttemptCount = outcome.Attempts
	state.JoinResult = outcome.Join
	if outcome.FailureReason != "" {
		reason := outcome.FailureReason
		state.FailureReason = &reason
	}
	if outcome.Status == domain.PhaseDegraded {
		state.Degraded = true
	}

	events := []domain.EventDraft{{
		Type:    phaseEvent(outcome.Status),
		Actor:   domain.ActorSystem,
		Phase:   phase.Name,
		Payload: phasePayload(outcome, label),
	}}
	if outcome.BudgetUnit != "" {
		events = append(events, domain.EventDraft{
			Type:  domain.EventPhaseBudgetExhausted,
			Actor: domain.ActorSystem,
			Phase: phase.Name,
			Payload: map[string]any{
				"unit":       outcome.BudgetUnit,
				"attempts":   outcome.Attempts,
				"consumed":   map[string]any{"attempts": s.spentNow().Attempts, "tokens": s.spentNow().Tokens()},
				"limit_note": "the declared " + outcome.BudgetUnit + " allowance was reached",
			},
		})
	}
	for _, ref := range outcome.Artifacts {
		events = append(events, domain.EventDraft{
			Type:    domain.EventArtifactCommitted,
			Actor:   domain.ActorSystem,
			Phase:   phase.Name,
			Payload: map[string]any{"sha256": ref.SHA256, "kind": ref.Kind, "size": ref.SizeBytes},
		})
	}

	decisions := make([]domain.DecisionDraft, 0, 2)
	if outcome.BudgetUnit != "" {
		decisions = append(decisions, domain.DecisionDraft{
			Phase:   phase.Name,
			Kind:    domain.DecisionBudgetStop,
			Summary: fmt.Sprintf("stopped %s: %s budget exhausted", phase.Name, outcome.BudgetUnit),
			Rationale: fmt.Sprintf("the %s allowance was reached after %d attempt(s), so no further attempt was started",
				outcome.BudgetUnit, outcome.Attempts),
			Evidence: map[string]any{
				"unit":       outcome.BudgetUnit,
				"attempts":   outcome.Attempts,
				"tokens_in":  s.spentNow().TokensIn,
				"tokens_out": s.spentNow().TokensOut,
			},
		})
	}
	if outcome.Join != nil {
		decisions = append(decisions, domain.DecisionDraft{
			Phase:   phase.Name,
			Kind:    domain.DecisionJoin,
			Summary: fmt.Sprintf("%s join %s", outcome.Join.Rule, satisfiedWord(outcome.Join.Satisfied)),
			Rationale: fmt.Sprintf("rule %q with %d of %d branches successful %s the join",
				outcome.Join.Rule, successfulBranches(outcome.Join), len(outcome.Join.Branches), satisfiedWord(outcome.Join.Satisfied)),
			Evidence: map[string]any{
				"rule":       outcome.Join.Rule,
				"n":          outcome.Join.N,
				"satisfied":  outcome.Join.Satisfied,
				"branches":   len(outcome.Join.Branches),
				"successful": successfulBranches(outcome.Join),
			},
		})
	}

	// Attempt-owned artifacts were committed with their attempt; only phase-level
	// rows (the fan-out aggregate) belong to this transition.
	phaseArtifacts := make([]domain.ArtifactRef, 0, 1)
	for _, ref := range outcome.Artifacts {
		if ref.AttemptID == nil {
			phaseArtifacts = append(phaseArtifacts, ref)
		}
	}

	delta := domain.RunDelta{SpentDelta: s.pendingSpend()}
	if err := s.commitPhase(ctx, *state, &delta, events, decisions, phaseArtifacts...); err != nil {
		return err
	}
	if s.observer != nil {
		s.observer.OnPhaseFinished(ctx, s.run.ID, phase.Name, outcome.Status)
	}
	s.recordResult(PhaseResult{
		Name:       phase.Name,
		Status:     outcome.Status,
		Attempts:   outcome.Attempts,
		Artifacts:  outcome.Artifacts,
		ErrorClass: outcome.ErrorClass,
	})
	return nil
}

// applyFailurePolicy decides what a failed or degraded phase does to the run.
func (s *session) applyFailurePolicy(ctx context.Context, index int, phase definition.Phase, outcome phaseOutcome) (*RunResult, error) {
	policy := phase.OnFailure
	reason := fmt.Sprintf("phase %q %s: %s", phase.Name, outcome.Status, outcome.FailureReason)
	decision := domain.DecisionDraft{
		Phase:   phase.Name,
		Kind:    domain.DecisionFailurePolicy,
		Summary: fmt.Sprintf("on_failure=%s applied to %s", policy, phase.Name),
		Rationale: fmt.Sprintf("phase ended %s with class %q, so policy %q was applied: %s",
			outcome.Status, outcome.ErrorClass, policy, policyEffect(policy)),
		Evidence: map[string]any{
			"policy":       string(policy),
			"phase_status": string(outcome.Status),
			"error_class":  string(outcome.ErrorClass),
		},
	}

	switch policy {
	case definition.FailContinue:
		// A degraded phase is a recorded, expected outcome; the run continues.
		return nil, s.commitRun(ctx, domain.RunDelta{},
			[]domain.EventDraft{}, []domain.DecisionDraft{decision}, false)
	case definition.FailEscalate, definition.FailAbort:
		if err := s.commitRun(ctx, domain.RunDelta{
			Status:        domain.RunFailed,
			OutcomeReason: &reason,
		}, []domain.EventDraft{{
			Type:    domain.EventRunFailed,
			Actor:   domain.ActorSystem,
			Phase:   phase.Name,
			Payload: map[string]any{"reason": reason, "error_class": string(outcome.ErrorClass)},
		}}, []domain.DecisionDraft{decision}, false); err != nil {
			return nil, err
		}
		s.releaseLease(context.WithoutCancel(ctx))

		// The root cause's hint is more useful than a generic one, so it wins when
		// present; the cause itself is wrapped so errors.Is still matches it.
		hint := domain.HintOf(outcome.Cause)
		if hint == "" {
			hint = fmt.Sprintf("inspect run %s, then resume after fixing phase %q", s.run.ID, phase.Name)
		}
		failure := &domain.Error{
			Class: outcome.ErrorClass,
			Msg:   reason,
			Hint:  hint,
			Err:   outcome.Cause,
		}
		result := s.result(failure)
		result.Status = domain.RunFailed
		return &result, nil
	default:
		return nil, fmt.Errorf("phase %q declares unknown failure policy %q", phase.Name, policy)
	}
}

// skipPhase records a when-condition that did not hold. The decision names the
// observed outcome so a reader can see why the work was skipped.
func (s *session) skipPhase(ctx context.Context, index int, phase definition.Phase) error {
	state, err := s.phaseState(ctx, phase.Name)
	if err != nil {
		return err
	}
	prior, err := s.phaseState(ctx, phase.When.AfterPhase)
	if err != nil {
		return err
	}
	ended := s.engine.now()
	state.Status = domain.PhaseSkipped
	state.StartedAt = &ended
	state.EndedAt = &ended

	rationale := fmt.Sprintf("when-condition required %q of phase %q but observed %q",
		phase.When.Outcome, phase.When.AfterPhase, prior.Status)
	return s.commitPhase(ctx, state, nil, []domain.EventDraft{{
		Type:  domain.EventPhaseSkipped,
		Actor: domain.ActorSystem,
		Phase: phase.Name,
		Payload: map[string]any{
			"after_phase": phase.When.AfterPhase,
			"required":    string(phase.When.Outcome),
			"observed":    string(prior.Status),
		},
	}}, []domain.DecisionDraft{{
		Phase:     phase.Name,
		Kind:      domain.DecisionSkip,
		Summary:   fmt.Sprintf("skipped %s because %s is %s", phase.Name, phase.When.AfterPhase, prior.Status),
		Rationale: rationale,
		Evidence: map[string]any{
			"after_phase": phase.When.AfterPhase,
			"required":    string(phase.When.Outcome),
			"observed":    string(prior.Status),
		},
	}})
}

// callModel performs one model interaction with the resolved request shape.
func (s *session) callModel(ctx context.Context, agent definition.AgentRef, phase definition.Phase, label string) (domain.ModelResponse, error) {
	client, err := s.engine.models.Client(agent.Model.Provider)
	if err != nil {
		return domain.ModelResponse{}, err
	}
	request := domain.ModelRequest{
		Model:           agent.Model.Name,
		System:          agent.Instructions,
		MaxOutputTokens: agent.Model.MaxOutputTokens,
		Temperature:     agent.Model.Temperature,
		Metadata: map[string]string{
			"run_id":  s.run.ID.String(),
			"phase":   phase.Name,
			"attempt": labelOrPhase(label, phase.Name),
		},
	}
	if request.MaxOutputTokens <= 0 {
		// The contract requires a positive limit; defaulting here would hide a
		// definition mistake that also affects what the adapter may send.
		request.MaxOutputTokens = 1024
	}
	return client.Complete(ctx, request)
}

// persistArtifacts writes attempt content to the artifact store and returns the
// descriptors the caller inserts with the attempt outcome.
func (s *session) persistArtifacts(ctx context.Context, phase definition.Phase, attemptID uuid.UUID, content, label string) ([]domain.ArtifactRef, error) {
	kinds := phase.Outputs
	if len(kinds) == 0 {
		kinds = []string{DefaultArtifactKind}
	}

	refs := make([]domain.ArtifactRef, 0, len(kinds))
	for _, kind := range kinds {
		body := content
		// A document is identified by its bytes, so two kinds served from one
		// model answer must differ in their stored form. The declared kind is
		// written as a marker line, and only when a phase serves several kinds —
		// which keeps the bytes of the common single-kind phase exactly what the
		// model produced.
		if len(kinds) > 1 {
			body = kind + "\n" + content
		}
		ref, err := s.engine.artifacts.Put(ctx, s.run.ID, kind, "text/markdown", strings.NewReader(body))
		if err != nil {
			return refs, err
		}
		ref.Phase = phase.Name
		ref.AttemptID = &attemptID
		refs = append(refs, ref)
	}
	return refs, nil
}

// attemptContext applies a phase or branch timeout to one model call. The timeout
// is per attempt, not per phase, so a retry gets its own budget of time (FR-006).
func (s *session) attemptContext(ctx context.Context, timeout *domain.Duration) (context.Context, context.CancelFunc) {
	if timeout == nil || timeout.Duration() <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout.Duration())
}

// phaseEvent maps a terminal phase status onto its event type.
func phaseEvent(status domain.PhaseStatus) domain.EventType {
	if event := domain.PhaseTerminalEvent(status); event != "" {
		return event
	}
	return domain.EventPhaseFailed
}

func phasePayload(outcome phaseOutcome, label string) map[string]any {
	payload := map[string]any{
		"status":   string(outcome.Status),
		"attempts": outcome.Attempts,
		"branch":   label,
	}
	if outcome.ErrorClass != "" {
		payload["error_class"] = string(outcome.ErrorClass)
	}
	if outcome.FailureReason != "" {
		payload["reason"] = outcome.FailureReason
	}
	if outcome.Join != nil {
		payload["join_satisfied"] = outcome.Join.Satisfied
	}
	return payload
}

// labelOrPhase keeps event payloads readable for single-agent phases.
func labelOrPhase(label, phase string) string {
	if label == "" {
		return phase
	}
	return label
}

func policyEffect(policy definition.FailurePolicy) string {
	switch policy {
	case definition.FailContinue:
		return "the run continues and the phase is recorded as degraded"
	case definition.FailEscalate:
		return "the run stops and the failure is escalated to the operator"
	default:
		return "the run stops"
	}
}

func satisfiedWord(satisfied bool) string {
	if satisfied {
		return "satisfied"
	}
	return "not satisfied"
}

func successfulBranches(join *domain.JoinResult) int {
	count := 0
	for _, branch := range join.Branches {
		if branch.Status == domain.AttemptSucceeded {
			count++
		}
	}
	return count
}
