package domain

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SchemaVersion is the persistence format this binary writes. A store row with a
// different version must be migrated with a recorded migration or refused (FR-017).
const SchemaVersion = 1

// Consumption is the accumulated spend of a run, phase, or attempt. Tokens are
// kept as integers because budgets are enforced on them exactly (FR-007).
type Consumption struct {
	Attempts    int   `json:"attempts"`
	WallClockMS int64 `json:"wall_clock_ms"`
	TokensIn    int   `json:"tokens_in"`
	TokensOut   int   `json:"tokens_out"`
}

// Add returns the sum, so accounting never mutates a shared value by accident.
func (c Consumption) Add(other Consumption) Consumption {
	return Consumption{
		Attempts:    c.Attempts + other.Attempts,
		WallClockMS: c.WallClockMS + other.WallClockMS,
		TokensIn:    c.TokensIn + other.TokensIn,
		TokensOut:   c.TokensOut + other.TokensOut,
	}
}

// Sub returns the consumption c has beyond other. It is how a writer derives the
// delta to record after accumulating locally: the store accumulates deltas, so
// committing an absolute total twice would double-count it. Fields that would go
// negative are floored at zero, because a negative delta can only mean the total
// was reset (a reload from the store), never real spend.
func (c Consumption) Sub(other Consumption) Consumption {
	return Consumption{
		Attempts:    floorZero(c.Attempts - other.Attempts),
		WallClockMS: floorZero64(c.WallClockMS - other.WallClockMS),
		TokensIn:    floorZero(c.TokensIn - other.TokensIn),
		TokensOut:   floorZero(c.TokensOut - other.TokensOut),
	}
}

func floorZero(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func floorZero64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// Tokens is the total token consumption, the unit the token budget is expressed in.
func (c Consumption) Tokens() int { return c.TokensIn + c.TokensOut }

// Duration is a time span that serialises as Go duration syntax with a single
// unit ("30s", "5m"), matching the definition schema's restricted pattern.
// A single unit is deliberate: "1m30s" has two reasonable spellings and would
// make canonical definition fingerprints unstable.
type Duration struct {
	d time.Duration
}

// NewDuration wraps a time.Duration.
func NewDuration(d time.Duration) Duration { return Duration{d: d} }

// Duration returns the underlying value.
func (d Duration) Duration() time.Duration { return d.d }

// String renders the value in the restricted single-unit syntax, choosing the
// largest unit that divides exactly. time.Duration.String would emit "5m0s" for
// five minutes, which the definition syntax forbids and which would make a parsed
// definition fail its own fingerprint round-trip.
func (d Duration) String() string {
	switch {
	case d.d%time.Hour == 0:
		return fmt.Sprintf("%dh", d.d/time.Hour)
	case d.d%time.Minute == 0:
		return fmt.Sprintf("%dm", d.d/time.Minute)
	case d.d%time.Second == 0:
		return fmt.Sprintf("%ds", d.d/time.Second)
	default:
		return fmt.Sprintf("%dms", d.d/time.Millisecond)
	}
}

// MarshalText lets YAML and TOML encoders emit the same syntax as JSON.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText lets YAML and TOML decoders apply the same restricted syntax as
// JSON, so all three formats accept exactly the same documents.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// MarshalJSON writes the value as a quoted duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts only the restricted single-unit syntax.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := ParseDuration(text)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// ParseDuration parses the restricted duration syntax of the definition schema:
// one or more digits followed by exactly one unit among ms, s, m, h.
func ParseDuration(text string) (Duration, error) {
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	if i == 0 || i == len(text) {
		return Duration{}, fmt.Errorf("duration %q must match <number><unit> with unit one of ms, s, m, h", text)
	}
	digits, unit := text[:i], text[i:]
	switch unit {
	case "ms", "s", "m", "h":
	default:
		return Duration{}, fmt.Errorf("duration %q has unit %q; accepted units are ms, s, m, h", text, unit)
	}
	parsed, err := time.ParseDuration(digits + unit)
	if err != nil {
		return Duration{}, fmt.Errorf("duration %q: %w", text, err)
	}
	if parsed <= 0 {
		return Duration{}, fmt.Errorf("duration %q must be positive", text)
	}
	return Duration{d: parsed}, nil
}

// Budget bounds a run or a phase by attempt count, wall-clock time, and tokens
// (FR-005, FR-007). A present Budget must set at least one allowance.
type Budget struct {
	Attempts  *int      `json:"attempts,omitempty"`
	WallClock *Duration `json:"wall_clock,omitempty"`
	Tokens    *int      `json:"tokens,omitempty"`
}

// IsZero reports whether no allowance is declared.
func (b Budget) IsZero() bool {
	return b.Attempts == nil && b.WallClock == nil && b.Tokens == nil
}

// Validate enforces the schema rules: at least one allowance, and each declared
// allowance is >= 1.
func (b Budget) Validate() error {
	if b.IsZero() {
		return fmt.Errorf("budget must declare at least one of attempts, wall_clock, tokens")
	}
	if b.Attempts != nil && *b.Attempts < 1 {
		return fmt.Errorf("budget.attempts must be >= 1, got %d", *b.Attempts)
	}
	if b.Tokens != nil && *b.Tokens < 1 {
		return fmt.Errorf("budget.tokens must be >= 1, got %d", *b.Tokens)
	}
	if b.WallClock != nil && b.WallClock.Duration() <= 0 {
		return fmt.Errorf("budget.wall_clock must be positive")
	}
	return nil
}

// Run is one execution of a workflow definition (data-model.md §2).
type Run struct {
	ID                    uuid.UUID
	DefinitionName        string
	DefinitionFingerprint string
	DefinitionJSON        []byte
	Status                RunStatus
	OwnerID               *string
	LeaseExpiresAt        *time.Time
	CurrentPhase          *string
	OutcomeReason         *string
	Budget                Budget
	Spent                 Consumption
	CreatedAt             time.Time
	UpdatedAt             time.Time
	RecordVersion         int
	SchemaVersion         int
}

// PhaseState is the persisted state of one phase inside a run (data-model.md §3).
type PhaseState struct {
	RunID         uuid.UUID
	Name          string
	Ordinal       int
	Status        PhaseStatus
	StartedAt     *time.Time
	EndedAt       *time.Time
	AttemptCount  int
	JoinResult    *JoinResult
	FailureReason *string
	Degraded      bool
}

// Attempt is one agent execution at a phase (data-model.md §4). It is the atomic
// unit of recovery: it commits wholly or is treated as not having happened.
type Attempt struct {
	ID               uuid.UUID
	RunID            uuid.UUID
	Phase            string
	AttemptNo        int
	Status           AttemptStatus
	AgentName        string
	ModelProvider    string
	ModelName        string
	InputFingerprint string
	PromptVersion    string
	StartedAt        time.Time
	EndedAt          *time.Time
	ErrorClass       ErrorClass
	ErrorDetail      *string
	Usage            Consumption
	RestartedFrom    *uuid.UUID
	Artifacts        []ArtifactRef
}

// ArtifactRef identifies produced content by its sha256 fingerprint (data-model.md §5).
type ArtifactRef struct {
	ID           uuid.UUID  `json:"id"`
	SHA256       string     `json:"sha256"`
	RunID        uuid.UUID  `json:"run_id"`
	Phase        string     `json:"phase"`
	AttemptID    *uuid.UUID `json:"attempt_id,omitempty"`
	Kind         string     `json:"kind"`
	MediaType    string     `json:"media_type"`
	SizeBytes    int64      `json:"size_bytes"`
	RelativePath string     `json:"relative_path"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Event is one immutable member of a run's totally ordered log (data-model.md §6).
// Seq is the only ordering authority; TS is informational (clock skew edge case).
type Event struct {
	Seq       int64
	RunID     uuid.UUID
	TS        time.Time
	Type      string
	Actor     string
	Phase     *string
	AttemptID *uuid.UUID
	Payload   json.RawMessage
	PrevHash  string
	Hash      string
}

// Decision is one recorded non-trivial choice and its rationale (data-model.md §7).
type Decision struct {
	ID        uuid.UUID
	RunID     uuid.UUID
	Phase     *string
	AttemptID *uuid.UUID
	TS        time.Time
	Kind      DecisionKind
	Summary   string
	Rationale string
	Evidence  json.RawMessage
}

// Lease is the time-bounded right to advance one run (ADR-0004).
type Lease struct {
	RunID         uuid.UUID
	Owner         string
	ExpiresAt     time.Time
	RecordVersion int
}

// BranchOutcome is one branch's result inside a fan-out join. It carries only the
// branch's durable identity — name, status, attempt number, and the digests of the
// artifacts it produced — never run-specific identifiers, so the aggregate
// document is byte-identical for two runs of the same workflow (FR-003).
type BranchOutcome struct {
	Branch      string        `json:"branch"`
	Status      AttemptStatus `json:"status"`
	AttemptNo   int           `json:"attempt_no"`
	ErrorClass  ErrorClass    `json:"error_class,omitempty"`
	ErrorDetail string        `json:"error_detail,omitempty"`
	Digests     []string      `json:"digests,omitempty"`
}

// JoinResult is the aggregation record of a fan-out phase. Branch order is the
// declared order, never completion order, so the aggregate artifact is
// deterministic (FR-003).
type JoinResult struct {
	Rule      string          `json:"rule"`
	N         int             `json:"n,omitempty"`
	Satisfied bool            `json:"satisfied"`
	Branches  []BranchOutcome `json:"branches"`
}

// EventDraft is an event that becomes durable inside the committing transaction.
type EventDraft struct {
	Type      EventType
	Actor     string
	Phase     string
	AttemptID uuid.UUID
	Payload   map[string]any
}

// DecisionDraft is a decision record that becomes durable with its transition.
type DecisionDraft struct {
	Phase     string
	AttemptID uuid.UUID
	Kind      DecisionKind
	Summary   string
	Rationale string
	Evidence  map[string]any
}
