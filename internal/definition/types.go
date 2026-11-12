// Package definition holds the workflow definition: its types, its declarative
// decoders for YAML/JSON/TOML, its programmatic builders, its canonical form, and
// its single validation path.
//
// Both entry points — a file on disk and a builder in code — converge on the same
// in-memory Definition and the same validation (FR-061). A definition is immutable
// once a run references it; the run stores a canonical snapshot and a fingerprint.
package definition

import "github.com/hIMEI29A/zamnius/internal/domain"

// SchemaVersion is the only definition schema version this build understands.
const SchemaVersion = "1"

// Mode selects how a phase executes.
type Mode string

// Phase modes.
const (
	ModeSingle Mode = "single"
	ModeFanout Mode = "fanout"
)

// FailurePolicy is what a run does when a phase cannot succeed.
type FailurePolicy string

// Failure policies.
const (
	FailAbort    FailurePolicy = "abort"
	FailContinue FailurePolicy = "continue"
	FailEscalate FailurePolicy = "escalate"
)

// JoinRule is how a fan-out phase decides its outcome.
type JoinRule string

// Join rules.
const (
	JoinAll  JoinRule = "all"
	JoinAny  JoinRule = "any"
	JoinNofM JoinRule = "n_of_m"
)

// ConditionOutcome is the outcome a when-condition tests for.
type ConditionOutcome string

// Condition outcomes.
const (
	OutcomeSucceeded ConditionOutcome = "succeeded"
	OutcomeFailed    ConditionOutcome = "failed"
	OutcomeAny       ConditionOutcome = "any"
)

// Definition is a validated workflow. Field names and tags match the declarative
// schema; the same struct is built in code and decoded from a file.
type Definition struct {
	Version     string         `json:"version" yaml:"version" toml:"version"`
	Name        string         `json:"name" yaml:"name" toml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty" toml:"description,omitempty"`
	Budget      *domain.Budget `json:"budget,omitempty" yaml:"budget,omitempty" toml:"budget,omitempty"`
	Defaults    *PhaseDefaults `json:"defaults,omitempty" yaml:"defaults,omitempty" toml:"defaults,omitempty"`
	Phases      []Phase        `json:"phases" yaml:"phases" toml:"phases"`
}

// PhaseDefaults are inherited by phases that omit a value.
type PhaseDefaults struct {
	Retry     *Retry           `json:"retry,omitempty" yaml:"retry,omitempty" toml:"retry,omitempty"`
	Timeout   *domain.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty" toml:"timeout,omitempty"`
	Budget    *domain.Budget   `json:"budget,omitempty" yaml:"budget,omitempty" toml:"budget,omitempty"`
	OnFailure FailurePolicy    `json:"on_failure,omitempty" yaml:"on_failure,omitempty" toml:"on_failure,omitempty"`
}

// Phase is one ordered step of a workflow.
type Phase struct {
	Name      string           `json:"name" yaml:"name" toml:"name"`
	Mode      Mode             `json:"mode" yaml:"mode" toml:"mode"`
	Agent     *AgentRef        `json:"agent,omitempty" yaml:"agent,omitempty" toml:"agent,omitempty"`
	Branches  []Branch         `json:"branches,omitempty" yaml:"branches,omitempty" toml:"branches,omitempty"`
	Join      *JoinPolicy      `json:"join,omitempty" yaml:"join,omitempty" toml:"join,omitempty"`
	DependsOn []string         `json:"depends_on,omitempty" yaml:"depends_on,omitempty" toml:"depends_on,omitempty"`
	When      *Condition       `json:"when,omitempty" yaml:"when,omitempty" toml:"when,omitempty"`
	Retry     *Retry           `json:"retry,omitempty" yaml:"retry,omitempty" toml:"retry,omitempty"`
	Timeout   *domain.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty" toml:"timeout,omitempty"`
	Budget    *domain.Budget   `json:"budget,omitempty" yaml:"budget,omitempty" toml:"budget,omitempty"`
	OnFailure FailurePolicy    `json:"on_failure,omitempty" yaml:"on_failure,omitempty" toml:"on_failure,omitempty"`
	Outputs   []string         `json:"outputs,omitempty" yaml:"outputs,omitempty" toml:"outputs,omitempty"`
}

// AgentRef is a named participant with instructions, a model, and its own budget.
// Role permission scoping belongs to the follow-on collaboration increment.
type AgentRef struct {
	Name         string         `json:"name" yaml:"name" toml:"name"`
	Instructions string         `json:"instructions" yaml:"instructions" toml:"instructions"`
	Model        ModelRef       `json:"model" yaml:"model" toml:"model"`
	Budget       *domain.Budget `json:"budget,omitempty" yaml:"budget,omitempty" toml:"budget,omitempty"`
}

// ModelRef names the provider and model an agent uses. The provider is resolved
// through a registry, never constructed here (FR-059).
type ModelRef struct {
	Provider        string   `json:"provider" yaml:"provider" toml:"provider"`
	Name            string   `json:"name" yaml:"name" toml:"name"`
	BaseURL         string   `json:"base_url,omitempty" yaml:"base_url,omitempty" toml:"base_url,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty" toml:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty" yaml:"temperature,omitempty" toml:"temperature,omitempty"`
}

// Branch is one parallel lane inside a fan-out phase.
type Branch struct {
	Name    string           `json:"name" yaml:"name" toml:"name"`
	Agent   AgentRef         `json:"agent" yaml:"agent" toml:"agent"`
	Budget  *domain.Budget   `json:"budget,omitempty" yaml:"budget,omitempty" toml:"budget,omitempty"`
	Timeout *domain.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty" toml:"timeout,omitempty"`
}

// JoinPolicy decides when a fan-out phase succeeds.
type JoinPolicy struct {
	Rule JoinRule `json:"rule" yaml:"rule" toml:"rule"`
	N    *int     `json:"n,omitempty" yaml:"n,omitempty" toml:"n,omitempty"`
}

// Retry bounds repeated attempts of the same phase.
type Retry struct {
	MaxAttempts int                 `json:"max_attempts" yaml:"max_attempts" toml:"max_attempts"`
	Backoff     *Backoff            `json:"backoff,omitempty" yaml:"backoff,omitempty" toml:"backoff,omitempty"`
	RetryOn     []domain.ErrorClass `json:"retry_on,omitempty" yaml:"retry_on,omitempty" toml:"retry_on,omitempty"`
}

// Backoff shapes the delay between attempts.
type Backoff struct {
	Initial    *domain.Duration `json:"initial,omitempty" yaml:"initial,omitempty" toml:"initial,omitempty"`
	Max        *domain.Duration `json:"max,omitempty" yaml:"max,omitempty" toml:"max,omitempty"`
	Multiplier *float64         `json:"multiplier,omitempty" yaml:"multiplier,omitempty" toml:"multiplier,omitempty"`
}

// Condition skips or runs a phase based on a recorded outcome. It is deliberately
// minimal in this increment: no expression language, no dependencies beyond one
// prior phase.
type Condition struct {
	AfterPhase string           `json:"after_phase" yaml:"after_phase" toml:"after_phase"`
	Outcome    ConditionOutcome `json:"outcome" yaml:"outcome" toml:"outcome"`
}

// Retry defaults. They are applied by Normalize so the engine never has to guess.
const (
	DefaultMaxAttempts      = 1
	DefaultBackoffInitialMS = 1000
	DefaultBackoffMaxMS     = 30000
	DefaultBackoffFactor    = 2.0
)

// AgentName is the resolved display name of a phase's agent, which is the branch
// name for a fan-out branch and the declared agent name otherwise.
func (p Phase) AgentName() string {
	if p.Agent != nil {
		return p.Agent.Name
	}
	return ""
}
