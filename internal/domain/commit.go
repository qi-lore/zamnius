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
	// SchemaVersion brings the row up to a migrated schema. Zero means unchanged;
	// it travels with the state.migrated event so the row and its history cannot
	// disagree about which format the run now conforms to (FR-011, FR-017).
	SchemaVersion int
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
//
// ExpectedVersion is the run's record_version the caller believes it holds; a
// non-zero value makes the commit conditional, so a writer that lost ownership
// fails with ErrStaleRecord instead of overwriting a newer owner's work
// (contracts/store.md §1.4).
type PhaseCommit struct {
	Phase           PhaseState
	Run             *RunDelta
	ExpectedVersion int
	// Artifacts are phase-level rows produced by the transition itself, such as a
	// fan-out aggregate. Attempt-owned artifacts travel with the attempt instead.
	Artifacts []ArtifactRef
	Events    []EventDraft
	Decisions []DecisionDraft
}

// RunCommit persists a run-level transition with its events and decisions.
// ExpectedVersion guards the run row exactly as in PhaseCommit.
type RunCommit struct {
	RunID           uuid.UUID
	Delta           RunDelta
	ExpectedVersion int
	Events          []EventDraft
	Decisions       []DecisionDraft
}
