package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/hIMEI29A/zamnius/internal/audit"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// reconcileAttempts repairs the one state a crash can leave behind: an attempt
// that started and never committed an outcome. It is marked abandoned with the
// restart cause recorded, so the audit can explain why a second attempt exists
// (data-model.md §4 invariant 2, FR-063).
func (s *session) reconcileAttempts(ctx context.Context, events []domain.Event) error {
	for _, phase := range s.def.Phases {
		attempts, err := s.engine.store.Attempts(ctx, s.run.ID, phase.Name)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
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
