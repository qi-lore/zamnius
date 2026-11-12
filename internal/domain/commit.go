package domain

import (
	"time"

	"github.com/google/uuid"
)

// RunSpec is everything a store needs to create a run atomically: the run row,
// its pending phase rows, and the canonical definition snapshot. Splitting these
// would allow a run to exist without the definition it is executing.
type RunSpec struct {
	ID                    uuid.UUID
	DefinitionName        string
	DefinitionFingerprint string
	DefinitionJSON        []byte
	Budget                Budget
	Phases                []PhaseState
	Now                   time.Time
}

// RunDelta is a partial run update applied inside a committing transaction.
// Empty status and nil pointers mean "leave unchanged"; that convention keeps
// callers from having to re-read the row and risking a stale write.
type RunDelta struct {
	Status        RunStatus
	CurrentPhase  *string
	OutcomeReason *string
	SpentDelta    Consumption
}

// AttemptCommit persists one attempt outcome: the attempt row, its artifacts,
// an optional updated phase row, and the events and decisions that describe it.
// They commit together or not at all (FR-011, FR-063).
type AttemptCommit struct {
	Attempt   Attempt
	Artifacts []ArtifactRef
	Phase     *PhaseState
	Events    []EventDraft
	Decisions []DecisionDraft
}

// PhaseCommit persists a phase transition, optionally with a run-level delta
// (for example the run's current position when the phase starts).
type PhaseCommit struct {
	Phase     PhaseState
	Run       *RunDelta
	Events    []EventDraft
	Decisions []DecisionDraft
}

// RunCommit persists a run-level transition with its events and decisions.
type RunCommit struct {
	RunID     uuid.UUID
	Delta     RunDelta
	Events    []EventDraft
	Decisions []DecisionDraft
}
