package engine

import (
	"context"
	"fmt"

	"github.com/prompt-fu/zamnius/internal/domain"
)

// pause stops the run in a resumable state. The commit runs on a context that is
// independent of the cancelled caller, because the reason for stopping must still
// be recorded (FR-008). When the stop was caused by a lost lease, that cause is
// recorded instead of a generic cancellation, so an operator can tell the two
// apart (T048, ADR-0004).
func (s *session) pause(ctx context.Context, reason string) (RunResult, error) {
	if cause := s.leaseLossCause(); cause != nil {
		return s.pauseWithCause(ctx, "lease renewal failed: ownership of the run was lost", cause)
	}
	return s.pauseWithCause(ctx, reason, context.Canceled)
}

// pauseWithCause records why the run stopped, releases the lease, and returns the
// paused result. The lease is released so another process may resume immediately
// instead of waiting for expiry.
func (s *session) pauseWithCause(ctx context.Context, reason string, cause error) (RunResult, error) {
	commitCtx, cancel := pauseContext(ctx)
	defer cancel()

	event := domain.EventDraft{
		Type:    domain.EventRunPaused,
		Actor:   domain.ActorSystem,
		Payload: map[string]any{"reason": reason, "cause": causeOf(cause)},
	}
	if err := s.commitRun(commitCtx, domain.RunDelta{Status: domain.RunPaused, OutcomeReason: &reason},
		[]domain.EventDraft{event}, nil, false); err != nil {
		// The run could not be marked paused; report the original cause and let the
		// lease expire rather than pretending the run reached a known state.
		s.engine.logger.Error("could not record pause", "run_id", s.run.ID.String(), "error", err.Error())
		result := s.result(cause)
		result.Status = s.run.Status
		return result, cause
	}

	s.releaseLease(commitCtx)

	failure := &domain.Error{
		Class: domain.ErrClassCancelled,
		Msg:   "run paused: " + reason,
		Hint:  fmt.Sprintf("resume run %s once the interruption is over", s.run.ID),
		Err:   cause,
	}
	result := s.result(failure)
	result.Status = domain.RunPaused
	s.engine.logger.Info("run paused", "run_id", s.run.ID.String(), "reason", reason)
	return result, failure
}
