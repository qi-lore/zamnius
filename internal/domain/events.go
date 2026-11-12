package domain

// EventType is the closed event vocabulary of this increment (data-model.md §6).
// It is closed on purpose: the audit completeness check (SC-005) asserts that
// every transition has a record, which is only decidable against a fixed set.
type EventType string

// The event vocabulary. Dotted names read as "<entity>.<what happened>".
const (
	EventRunCreated         EventType = "run.created"
	EventRunStarted         EventType = "run.started"
	EventRunPaused          EventType = "run.paused"
	EventRunResumed         EventType = "run.resumed"
	EventRunTakenOver       EventType = "run.taken_over"
	EventRunCancelRequested EventType = "run.cancel_requested"
	EventRunSucceeded       EventType = "run.succeeded"
	EventRunFailed          EventType = "run.failed"
	EventRunCancelled       EventType = "run.cancelled"

	EventPhaseStarted         EventType = "phase.started"
	EventPhaseSucceeded       EventType = "phase.succeeded"
	EventPhaseFailed          EventType = "phase.failed"
	EventPhaseDegraded        EventType = "phase.degraded"
	EventPhaseSkipped         EventType = "phase.skipped"
	EventPhaseBudgetExhausted EventType = "phase.budget_exhausted"

	EventAttemptStarted   EventType = "attempt.started"
	EventAttemptSucceeded EventType = "attempt.succeeded"
	EventAttemptFailed    EventType = "attempt.failed"
	EventAttemptAbandoned EventType = "attempt.abandoned"

	EventArtifactCommitted EventType = "artifact.committed"
	EventArtifactVerified  EventType = "artifact.verified"

	EventDecisionRecorded      EventType = "decision.recorded"
	EventStateMigrated         EventType = "state.migrated"
	EventStateMigrationRefused EventType = "state.migration_refused"
)

var allEventTypes = []EventType{
	EventRunCreated, EventRunStarted, EventRunPaused, EventRunResumed,
	EventRunTakenOver, EventRunCancelRequested, EventRunSucceeded, EventRunFailed,
	EventRunCancelled,
	EventPhaseStarted, EventPhaseSucceeded, EventPhaseFailed, EventPhaseDegraded,
	EventPhaseSkipped, EventPhaseBudgetExhausted,
	EventAttemptStarted, EventAttemptSucceeded, EventAttemptFailed, EventAttemptAbandoned,
	EventArtifactCommitted, EventArtifactVerified,
	EventDecisionRecorded, EventStateMigrated, EventStateMigrationRefused,
}

// Valid reports whether t is part of the vocabulary.
func (t EventType) Valid() bool {
	for _, candidate := range allEventTypes {
		if t == candidate {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (t EventType) String() string { return string(t) }

// Event type for a run-level transition, so callers never build the string by hand.
func RunTerminalEvent(status RunStatus) EventType {
	switch status {
	case RunSucceeded:
		return EventRunSucceeded
	case RunFailed:
		return EventRunFailed
	case RunCancelled:
		return EventRunCancelled
	default:
		return ""
	}
}

// PhaseTerminalEvent maps a terminal phase status onto its event type.
func PhaseTerminalEvent(status PhaseStatus) EventType {
	switch status {
	case PhaseSucceeded:
		return EventPhaseSucceeded
	case PhaseFailed:
		return EventPhaseFailed
	case PhaseDegraded:
		return EventPhaseDegraded
	case PhaseSkipped:
		return EventPhaseSkipped
	case PhaseCancelled:
		// Cancellation is expressed on the run; the phase records it without a
		// dedicated vocabulary entry in this increment.
		return EventPhaseFailed
	default:
		return ""
	}
}

// Actor names. The audit record must always answer "who", so actors are
// constructed, never typed inline at call sites.
const (
	ActorSystem = "system"
)

// ActorOperator returns the actor string for a human-initiated action.
func ActorOperator(id string) string { return "operator:" + id }

// ActorAgent returns the actor string for an agent action.
func ActorAgent(name string) string { return "agent:" + name }
