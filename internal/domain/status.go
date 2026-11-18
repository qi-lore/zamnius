// Package domain holds the durable vocabulary of Zamnius: identifiers, state
// machines, error classes, and the records the store persists.
//
// Nothing in this package may import a transport, a database driver, or a model
// provider (constitution X.2). The status sets are closed on purpose: the store
// schema has CHECK constraints mirroring them, and resume logic branches on them.
package domain

// RunStatus is the lifecycle state of a run (data-model.md §2).
//
//	pending -> running -> succeeded | failed | cancelled
//	                  \-> paused -> running
type RunStatus string

// Run lifecycle states.
const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunPaused    RunStatus = "paused"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// allRunStatuses is the closed set; ordering is irrelevant, membership is not.
var allRunStatuses = []RunStatus{
	RunPending, RunRunning, RunPaused, RunSucceeded, RunFailed, RunCancelled,
}

// Valid reports whether s is one of the persisted run states.
func (s RunStatus) Valid() bool {
	for _, candidate := range allRunStatuses {
		if s == candidate {
			return true
		}
	}
	return false
}

// Terminal reports whether no further transition is allowed. Terminal runs are
// immutable and resume on them is an idempotent no-op (FR-018).
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCancelled
}

// String implements fmt.Stringer so logs never print a raw alias type.
func (s RunStatus) String() string { return string(s) }

// PhaseStatus is the lifecycle state of one phase within a run (data-model.md §3).
type PhaseStatus string

// Phase lifecycle states.
const (
	PhasePending   PhaseStatus = "pending"
	PhaseRunning   PhaseStatus = "running"
	PhaseSucceeded PhaseStatus = "succeeded"
	PhaseFailed    PhaseStatus = "failed"
	PhaseDegraded  PhaseStatus = "degraded"
	PhaseCancelled PhaseStatus = "cancelled"
	PhaseSkipped   PhaseStatus = "skipped"
)

var allPhaseStatuses = []PhaseStatus{
	PhasePending, PhaseRunning, PhaseSucceeded, PhaseFailed,
	PhaseDegraded, PhaseCancelled, PhaseSkipped,
}

// Valid reports whether s is one of the persisted phase states.
func (s PhaseStatus) Valid() bool {
	for _, candidate := range allPhaseStatuses {
		if s == candidate {
			return true
		}
	}
	return false
}

// Terminal reports whether the phase has reached an outcome. Only terminal
// phases satisfy a depends_on edge or a when-condition.
func (s PhaseStatus) Terminal() bool {
	switch s {
	case PhaseSucceeded, PhaseFailed, PhaseDegraded, PhaseCancelled, PhaseSkipped:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (s PhaseStatus) String() string { return string(s) }

// AttemptStatus is the state of one step attempt (data-model.md §4).
type AttemptStatus string

// Attempt lifecycle states. There is no "pending": an attempt row exists only
// once its execution begins, and commits its outcome atomically (ADR-0003).
const (
	AttemptRunning   AttemptStatus = "running"
	AttemptSucceeded AttemptStatus = "succeeded"
	AttemptFailed    AttemptStatus = "failed"
	AttemptAbandoned AttemptStatus = "abandoned"
)

var allAttemptStatuses = []AttemptStatus{
	AttemptRunning, AttemptSucceeded, AttemptFailed, AttemptAbandoned,
}

// Valid reports whether s is one of the persisted attempt states.
func (s AttemptStatus) Valid() bool {
	for _, candidate := range allAttemptStatuses {
		if s == candidate {
			return true
		}
	}
	return false
}

// Terminal reports whether the attempt produced a committed outcome.
func (s AttemptStatus) Terminal() bool {
	return s == AttemptSucceeded || s == AttemptFailed || s == AttemptAbandoned
}

// String implements fmt.Stringer.
func (s AttemptStatus) String() string { return string(s) }

// ErrorClass is the closed classification every model call error carries
// (contracts/model-client.md §4). Budgets and retries are decided from it,
// so an unclassified error is a bug rather than a default.
type ErrorClass string

// The closed error-class set.
const (
	ErrClassTransient       ErrorClass = "transient"
	ErrClassRateLimited     ErrorClass = "rate_limited"
	ErrClassAuth            ErrorClass = "auth"
	ErrClassPermanent       ErrorClass = "permanent"
	ErrClassContextOverflow ErrorClass = "context_overflow"
	ErrClassInvalidResponse ErrorClass = "invalid_response"
	ErrClassTimeout         ErrorClass = "timeout"
	ErrClassBudgetExhausted ErrorClass = "budget_exhausted"
	ErrClassCancelled       ErrorClass = "cancelled"
	ErrClassInternal        ErrorClass = "internal"
)

var allErrorClasses = []ErrorClass{
	ErrClassTransient, ErrClassRateLimited, ErrClassAuth, ErrClassPermanent,
	ErrClassContextOverflow, ErrClassInvalidResponse, ErrClassTimeout,
	ErrClassBudgetExhausted, ErrClassCancelled, ErrClassInternal,
}

// Valid reports whether c is part of the closed set.
func (c ErrorClass) Valid() bool {
	for _, candidate := range allErrorClasses {
		if c == candidate {
			return true
		}
	}
	return false
}

// DefaultRetryable reports the class default before a phase's retry_on list is
// consulted. A declared retry_on list always wins; this only documents intent.
func (c ErrorClass) DefaultRetryable() bool {
	switch c {
	case ErrClassTransient, ErrClassRateLimited, ErrClassTimeout:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (c ErrorClass) String() string { return string(c) }

// DecisionKind is the closed set of recorded non-trivial choices
// (data-model.md §7). A decision record exists for every member of this set.
type DecisionKind string

// The decision kinds this increment can record.
const (
	DecisionRetry         DecisionKind = "retry"
	DecisionJoin          DecisionKind = "join"
	DecisionFailurePolicy DecisionKind = "failure_policy"
	DecisionBudgetStop    DecisionKind = "budget_stop"
	DecisionTakeover      DecisionKind = "takeover"
	DecisionSkip          DecisionKind = "skip"
	DecisionResumeRestart DecisionKind = "resume_restart"
)

var allDecisionKinds = []DecisionKind{
	DecisionRetry, DecisionJoin, DecisionFailurePolicy, DecisionBudgetStop,
	DecisionTakeover, DecisionSkip, DecisionResumeRestart,
}

// Valid reports whether k is one of the recorded decision kinds.
func (k DecisionKind) Valid() bool {
	for _, candidate := range allDecisionKinds {
		if k == candidate {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (k DecisionKind) String() string { return string(k) }
