package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/hIMEI29A/zamnius/internal/audit"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// reconcileAttempts repairs the two states a crash can leave behind. An attempt
// that started and never committed an outcome is marked abandoned with the restart
// cause recorded, and the attempt that replaces it is linked back through
// restarted_from. An attempt that did commit a success is remembered, so the run
// reuses its result instead of executing the step again (data-model.md §4
// invariant 2, FR-013, FR-063).
func (s *session) reconcileAttempts(ctx context.Context, events []domain.Event) error {
	for _, phase := range s.def.Phases {
		attempts, err := s.engine.store.Attempts(ctx, s.run.ID, phase.Name)
		if err != nil {
			return err
		}

		// latest is the highest-numbered attempt of each agent, and succeeded marks
		// agents whose work is already committed. Only the latest attempt can be the
		// one a resume restarts: an agent with a committed attempt is reused, and a
		// failed attempt is a normal retry rather than a crash restart.
		latest := make(map[string]domain.Attempt, len(attempts))
		succeeded := make(map[string]bool, len(attempts))

		for _, attempt := range attempts {
			if current, ok := latest[attempt.AgentName]; !ok || attempt.AttemptNo > current.AttemptNo {
				latest[attempt.AgentName] = attempt
			}
			if attempt.Status == domain.AttemptSucceeded {
				succeeded[attempt.AgentName] = true
				s.recordRecovered(phase.Name, attempt)
				continue
			}
			if attempt.Status != domain.AttemptRunning {
				continue
			}

			now := s.engine.now()
			detail := "attempt started but no outcome was committed before the run stopped"
			abandoned := attempt
			abandoned.Status = domain.AttemptAbandoned
			abandoned.EndedAt = &now
			abandoned.ErrorDetail = &detail

			if err := s.commitAttempt(ctx, abandoned, nil, []domain.EventDraft{{
				Type:      domain.EventAttemptAbandoned,
				Actor:     domain.ActorSystem,
				Phase:     phase.Name,
				AttemptID: attempt.ID,
				Payload: map[string]any{
					"attempt_no": attempt.AttemptNo,
					"started_at": domain.FormatTimestamp(attempt.StartedAt),
					"last_event": lastEventTypeFor(events, attempt.ID),
					"reason":     detail,
				},
			}}, []domain.DecisionDraft{{
				Phase:     phase.Name,
				AttemptID: attempt.ID,
				Kind:      domain.DecisionResumeRestart,
				Summary:   fmt.Sprintf("attempt %d of %s was abandoned and will be restarted", attempt.AttemptNo, phase.Name),
				Rationale: fmt.Sprintf("attempt %d of phase %q had status running with no committed outcome; the run stopped after event %q, so the attempt is treated as not having happened and is restarted from the phase's recorded inputs",
					attempt.AttemptNo, phase.Name, lastEventTypeFor(events, attempt.ID)),
				Evidence: map[string]any{
					"attempt_no": attempt.AttemptNo,
					"phase":      phase.Name,
					"started_at": domain.FormatTimestamp(attempt.StartedAt),
					"cause":      detail,
				},
			}}); err != nil {
				return err
			}
			s.engine.logger.Warn("reconciled an interrupted attempt",
				"run_id", s.run.ID.String(), "phase", phase.Name, "attempt_no", attempt.AttemptNo)
		}

		// The attempt a resume will replace is the latest one of an agent that has no
		// committed outcome: either it was running (just marked abandoned above) or it
		// was already abandoned when the owner paused. The replacement claims it as
		// restarted_from, so the two rows are linked (data-model.md §4 invariant 2).
		for agent, attempt := range latest {
			if succeeded[agent] {
				continue
			}
			if attempt.Status == domain.AttemptRunning || attempt.Status == domain.AttemptAbandoned {
				s.recordRestartSource(phase.Name, agent, attempt.ID)
			}
		}
	}
	return nil
}

// lastEventTypeFor names the last event of an attempt, which is the evidence that
// the attempt never reached its committing transaction.
func lastEventTypeFor(events []domain.Event, attemptID interface{ String() string }) string {
	wanted := attemptID.String()
	last := ""
	for _, event := range events {
		if event.AttemptID != nil && event.AttemptID.String() == wanted {
			last = event.Type
		}
	}
	if last == "" {
		return "none"
	}
	return last
}

// failOnIntegrity fails a run whose persisted record cannot be trusted, recording
// the evidence instead of resuming on partial knowledge (contracts/store.md §5).
func (s *session) failOnIntegrity(ctx context.Context, reason string, problems []audit.Problem) (RunResult, error) {
	details := make([]string, 0, len(problems))
	for _, problem := range problems {
		details = append(details, problem.Detail)
	}
	summary := reason + ": " + strings.Join(details, "; ")

	commitCtx, cancel := pauseContext(ctx)
	defer cancel()

	if err := s.commitRun(commitCtx, domain.RunDelta{Status: domain.RunFailed, OutcomeReason: &summary},
		[]domain.EventDraft{{
			Type:    domain.EventRunFailed,
			Actor:   domain.ActorSystem,
			Payload: map[string]any{"reason": reason, "problems": len(problems), "detail": summary},
		}}, nil, false); err != nil {
		failure := &domain.Error{Class: domain.ErrClassInternal, Msg: reason, Hint: summary, Err: domain.ErrIntegrity}
		return s.result(failure), failure
	}
	s.releaseLease(commitCtx)

	failure := &domain.Error{
		Class: domain.ErrClassInternal,
		Msg:   reason,
		Hint:  summary,
		Err:   domain.ErrIntegrity,
	}
	result := s.result(failure)
	result.Status = domain.RunFailed
	return result, failure
}
