// Package zamnius is the adopter-facing surface of the framework: the engine
// constructor, the stable contracts, and the exported error vocabulary.
//
// Everything importable lives here and is versioned under semantic versioning
// (contracts/library-api.md §5). Implementations stay under internal/; the type
// aliases below exist so adopters can name durable types without importing an
// internal package, while a single definition keeps the store and the engine
// from drifting apart.
package zamnius

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// Durable vocabulary. These are aliases, not copies: a value produced by the
// store is the same type an adopter holds.
type (
	// Run is one execution of a workflow definition.
	Run = domain.Run
	// PhaseState is the persisted state of one phase inside a run.
	PhaseState = domain.PhaseState
	// Attempt is one agent execution at a phase; the atomic unit of recovery.
	Attempt = domain.Attempt
	// ArtifactRef identifies produced content by its sha256 fingerprint.
	ArtifactRef = domain.ArtifactRef
	// Event is one immutable member of a run's ordered log.
	Event = domain.Event
	// Decision is one recorded non-trivial choice and its rationale.
	Decision = domain.Decision
	// Lease is the time-bounded right to advance one run.
	Lease = domain.Lease
	// Consumption is accumulated spend.
	Consumption = domain.Consumption
	// Budget bounds a run or phase by attempts, wall clock, and tokens.
	Budget = domain.Budget
	// Duration is a single-unit time span as written in a definition.
	Duration = domain.Duration
	// RunSpec is the input to store.CreateRun.
	RunSpec = domain.RunSpec
	// AttemptCommit is the atomic persistence unit for one attempt outcome.
	AttemptCommit = domain.AttemptCommit
	// PhaseCommit is the atomic persistence unit for one phase transition.
	PhaseCommit = domain.PhaseCommit
	// RunCommit is the atomic persistence unit for one run transition.
	RunCommit = domain.RunCommit
	// RunDelta is a partial run update applied inside a commit.
	RunDelta = domain.RunDelta
	// EventDraft is an event awaiting its committing transaction.
	EventDraft = domain.EventDraft
	// DecisionDraft is a decision awaiting its committing transaction.
	DecisionDraft = domain.DecisionDraft
	// JoinResult is the aggregation record of a fan-out phase.
	JoinResult = domain.JoinResult
	// BranchOutcome is one branch's result inside a join.
	BranchOutcome = domain.BranchOutcome
	// ModelRequest is the provider-neutral model request.
	ModelRequest = domain.ModelRequest
	// ModelResponse is the provider-neutral model answer.
	ModelResponse = domain.ModelResponse
	// Message is one conversational turn.
	Message = domain.Message
	// Usage is token consumption reported by a provider.
	Usage = domain.Usage
	// Role is a message author.
	Role = domain.Role
	// StopReason explains why generation stopped.
	StopReason = domain.StopReason
)

// Status and classification vocabulary.
type (
	// RunStatus is the lifecycle state of a run.
	RunStatus = domain.RunStatus
	// PhaseStatus is the lifecycle state of a phase.
	PhaseStatus = domain.PhaseStatus
	// AttemptStatus is the state of one step attempt.
	AttemptStatus = domain.AttemptStatus
	// ErrorClass is the closed error classification.
	ErrorClass = domain.ErrorClass
	// DecisionKind is the closed set of recorded choices.
	DecisionKind = domain.DecisionKind
	// EventType is the closed event vocabulary.
	EventType = domain.EventType
	// Error is the classified error every layer returns.
	Error = domain.Error
)

// Run statuses.
const (
	RunPending   = domain.RunPending
	RunRunning   = domain.RunRunning
	RunPaused    = domain.RunPaused
	RunSucceeded = domain.RunSucceeded
	RunFailed    = domain.RunFailed
	RunCancelled = domain.RunCancelled
)

// Phase statuses.
const (
	PhasePending   = domain.PhasePending
	PhaseRunning   = domain.PhaseRunning
	PhaseSucceeded = domain.PhaseSucceeded
	PhaseFailed    = domain.PhaseFailed
	PhaseDegraded  = domain.PhaseDegraded
	PhaseCancelled = domain.PhaseCancelled
	PhaseSkipped   = domain.PhaseSkipped
)

// Attempt statuses.
const (
	AttemptRunning   = domain.AttemptRunning
	AttemptSucceeded = domain.AttemptSucceeded
	AttemptFailed    = domain.AttemptFailed
	AttemptAbandoned = domain.AttemptAbandoned
)

// Error classes.
const (
	ErrClassTransient       = domain.ErrClassTransient
	ErrClassRateLimited     = domain.ErrClassRateLimited
	ErrClassAuth            = domain.ErrClassAuth
	ErrClassPermanent       = domain.ErrClassPermanent
	ErrClassContextOverflow = domain.ErrClassContextOverflow
	ErrClassInvalidResponse = domain.ErrClassInvalidResponse
	ErrClassTimeout         = domain.ErrClassTimeout
	ErrClassBudgetExhausted = domain.ErrClassBudgetExhausted
	ErrClassCancelled       = domain.ErrClassCancelled
	ErrClassInternal        = domain.ErrClassInternal
)

// Decision kinds.
const (
	DecisionRetry         = domain.DecisionRetry
	DecisionJoin          = domain.DecisionJoin
	DecisionFailurePolicy = domain.DecisionFailurePolicy
	DecisionBudgetStop    = domain.DecisionBudgetStop
	DecisionTakeover      = domain.DecisionTakeover
	DecisionSkip          = domain.DecisionSkip
	DecisionResumeRestart = domain.DecisionResumeRestart
)

// Message roles and stop reasons.
const (
	RoleUser      = domain.RoleUser
	RoleAssistant = domain.RoleAssistant

	StopEnd           = domain.StopEnd
	StopLength        = domain.StopLength
	StopContentFilter = domain.StopContentFilter
	StopUnknown       = domain.StopUnknown
)

// SchemaVersion is the persistence format this build writes.
const SchemaVersion = domain.SchemaVersion

// StateStore is the durable state port (contracts/store.md §2). Acquisition of a
// lease is a single conditional statement, never a read followed by a write, so
// two processes cannot both believe they own a run.
type StateStore interface {
	CreateRun(ctx context.Context, spec RunSpec) (Run, error)
	LoadRun(ctx context.Context, id uuid.UUID) (Run, error)

	AcquireLease(ctx context.Context, id uuid.UUID, owner string, ttl time.Duration) (Lease, error)
	RenewLease(ctx context.Context, l Lease, ttl time.Duration) (Lease, error)
	ReleaseLease(ctx context.Context, l Lease) error

	CommitAttemptResult(ctx context.Context, r AttemptCommit) error
	CommitPhaseResult(ctx context.Context, r PhaseCommit) error
	CommitRunResult(ctx context.Context, r RunCommit) error

	Phases(ctx context.Context, runID uuid.UUID) ([]PhaseState, error)
	Attempts(ctx context.Context, runID uuid.UUID, phase string) ([]Attempt, error)
	Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]Event, error)
	Decisions(ctx context.Context, runID uuid.UUID) ([]Decision, error)
}

// ArtifactStore is the content-addressed artifact port (contracts/store.md §4).
// Put is idempotent by content hash and writes the file durably before returning,
// so the caller may insert its row in the same transaction as the attempt.
type ArtifactStore interface {
	Put(ctx context.Context, runID uuid.UUID, kind, mediaType string, r io.Reader) (ArtifactRef, error)
	// Get verifies the fingerprint before returning any bytes.
	Get(ctx context.Context, sha string) (io.ReadCloser, error)
	Verify(ctx context.Context, sha string) error
}

// ModelClient completes one model interaction (contracts/model-client.md §1).
// A returned error must be classified, and usage must be populated: budgets
// depend on it, so a missing usage block is invalid_response rather than zero.
type ModelClient interface {
	Complete(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

// ModelRegistry resolves a provider name to a client, so a workflow definition
// names a provider but never constructs one (FR-059).
type ModelRegistry interface {
	Client(provider string) (ModelClient, error)
}

// Observer receives ordered engine facts. Implementations must not block the
// engine; buffering or dropping is their responsibility.
type Observer interface {
	OnPhaseStarted(ctx context.Context, runID uuid.UUID, phase string)
	OnAttemptFinished(ctx context.Context, runID uuid.UUID, phase string, attempt int, class ErrorClass)
	OnPhaseFinished(ctx context.Context, runID uuid.UUID, phase string, status PhaseStatus)
}
