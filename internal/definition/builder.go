package definition

import "github.com/hIMEI29A/zamnius/internal/domain"

// Builder assembles a definition in code. It exists so the programmatic path
// produces exactly what the declarative path produces, including defaults and
// validation: Build funnels into Normalize, never around it (FR-061).
type Builder struct {
	def Definition
}

// NewBuilder starts a definition with the current schema version.
func NewBuilder(name string) *Builder {
	return &Builder{def: Definition{Version: SchemaVersion, Name: name}}
}

// Describe sets the optional description.
func (b *Builder) Describe(description string) *Builder {
	b.def.Description = description
	return b
}

// WithBudget sets the run-level allowance.
func (b *Builder) WithBudget(budget domain.Budget) *Builder {
	b.def.Budget = &budget
	return b
}

// WithDefaults sets the values inherited by phases that omit them.
func (b *Builder) WithDefaults(defaults PhaseDefaults) *Builder {
	b.def.Defaults = &defaults
	return b
}

// WithPhase appends a single-agent phase.
func (b *Builder) WithPhase(name string, agent AgentRef, options ...PhaseOption) *Builder {
	phase := Phase{Name: name, Mode: ModeSingle, Agent: &agent}
	for _, option := range options {
		option(&phase)
	}
	b.def.Phases = append(b.def.Phases, phase)
	return b
}

// WithFanout appends a fan-out phase. The join policy is required by validation,
// so it is an explicit argument rather than an option.
func (b *Builder) WithFanout(name string, join JoinPolicy, branches []Branch, options ...PhaseOption) *Builder {
	phase := Phase{Name: name, Mode: ModeFanout, Join: &join, Branches: branches}
	for _, option := range options {
		option(&phase)
	}
	b.def.Phases = append(b.def.Phases, phase)
	return b
}

// Build validates and resolves the definition.
func (b *Builder) Build() (Definition, error) {
	return Normalize(b.def)
}

// PhaseOption customises a phase added by the builder.
type PhaseOption func(*Phase)

// When adds a skip condition to a phase.
func When(afterPhase string, outcome ConditionOutcome) PhaseOption {
	return func(phase *Phase) {
		phase.When = &Condition{AfterPhase: afterPhase, Outcome: outcome}
	}
}

// DependsOn overrides the default dependency on the previous phase.
func DependsOn(phases ...string) PhaseOption {
	return func(phase *Phase) { phase.DependsOn = phases }
}

// WithRetry sets the retry policy of a phase.
func WithRetry(retry Retry) PhaseOption {
	return func(phase *Phase) { phase.Retry = &retry }
}

// WithTimeout caps the wall-clock time of a phase.
func WithTimeout(timeout domain.Duration) PhaseOption {
	return func(phase *Phase) { phase.Timeout = &timeout }
}

// WithPhaseBudget bounds one phase.
func WithPhaseBudget(budget domain.Budget) PhaseOption {
	return func(phase *Phase) { phase.Budget = &budget }
}

// OnFailure sets the declared failure policy of a phase.
func OnFailure(policy FailurePolicy) PhaseOption {
	return func(phase *Phase) { phase.OnFailure = policy }
}

// WithOutputs declares the artifact kinds a phase promises. A phase only
// succeeds when each declared kind has a committed artifact.
func WithOutputs(kinds ...string) PhaseOption {
	return func(phase *Phase) { phase.Outputs = kinds }
}

// Agent builds an agent reference. maxOutputTokens above zero is applied.
func Agent(name, instructions, modelName string, maxOutputTokens int) AgentRef {
	return AgentRef{
		Name:         name,
		Instructions: instructions,
		Model: ModelRef{
			Provider:        "openai-compatible",
			Name:            modelName,
			MaxOutputTokens: maxOutputTokens,
		},
	}
}

// BranchOf builds a branch around an agent, using the agent's name as the branch
// name when the caller does not need a distinct one.
func BranchOf(name string, agent AgentRef) Branch {
	return Branch{Name: name, Agent: agent}
}
