package definition

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/prompt-fu/zamnius/internal/domain"
)

var (
	namePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	outputKindPattern = regexp.MustCompile(`^(plan|diff|report|evidence|aggregate|log|custom:[a-z0-9-]+)$`)
)

// Limits from contracts/workflow-definition.schema.json. They live here as
// constants so the decoder, the builder, and the error messages cannot disagree.
const (
	MaxDefinitionNameLength = 120
	MaxPhaseNameLength      = 80
	MaxDescriptionLength    = 2000
	MaxInstructionsLength   = 20000
	MinBranches             = 2
	MaxBranches             = 32
	MaxRetryAttempts        = 20
	MinBackoffMultiplier    = 1.0
	MaxBackoffMultiplier    = 10.0
)

// Normalize validates a definition and returns the fully resolved copy: defaults
// applied, retry defaults filled in, and depends_on defaulted to the previous
// phase. It is the single validation path for both entry points (FR-061), and the
// returned definition is what a run snapshots and fingerprints.
func Normalize(def Definition) (Definition, error) {
	if def.Version == "" {
		return Definition{}, invalid("version", "is required and must be %q", SchemaVersion)
	}
	if def.Version != SchemaVersion {
		return Definition{}, invalid("version", "must be %q, got %q", SchemaVersion, def.Version)
	}
	if err := checkName("name", def.Name, MaxDefinitionNameLength); err != nil {
		return Definition{}, err
	}
	if len(def.Description) > MaxDescriptionLength {
		return Definition{}, invalid("description", "must be at most %d characters, got %d",
			MaxDescriptionLength, len(def.Description))
	}
	if def.Budget != nil {
		if err := def.Budget.Validate(); err != nil {
			return Definition{}, invalid("budget", "%v", err)
		}
	}
	if def.Defaults != nil {
		if err := validateDefaults(*def.Defaults); err != nil {
			return Definition{}, err
		}
	}
	if len(def.Phases) == 0 {
		return Definition{}, invalid("phases", "must contain at least one phase")
	}

	seen := make(map[string]int, len(def.Phases))
	for i, phase := range def.Phases {
		if err := checkName(fmt.Sprintf("phases[%d].name", i), phase.Name, MaxPhaseNameLength); err != nil {
			return Definition{}, err
		}
		if previous, exists := seen[phase.Name]; exists {
			return Definition{}, invalid(fmt.Sprintf("phases[%d].name", i),
				"duplicates phases[%d].name %q; phase names must be unique", previous, phase.Name)
		}
		seen[phase.Name] = i
	}

	resolved := def
	resolved.Phases = make([]Phase, len(def.Phases))

	for i, phase := range def.Phases {
		path := fmt.Sprintf("phases[%d]", i)
		out, err := normalizePhase(path, i, phase, def, seen)
		if err != nil {
			return Definition{}, err
		}
		resolved.Phases[i] = out
	}
	return resolved, nil
}

func normalizePhase(path string, index int, phase Phase, def Definition, seen map[string]int) (Phase, error) {
	switch phase.Mode {
	case ModeSingle:
		if phase.Agent == nil {
			return Phase{}, invalid(path+".agent", "is required when mode is %q", ModeSingle)
		}
		if len(phase.Branches) > 0 {
			return Phase{}, invalid(path+".branches", "is only allowed when mode is %q", ModeFanout)
		}
		if phase.Join != nil {
			return Phase{}, invalid(path+".join", "is only allowed when mode is %q", ModeFanout)
		}
		if err := validateAgent(path+".agent", *phase.Agent); err != nil {
			return Phase{}, err
		}
	case ModeFanout:
		if phase.Agent != nil {
			return Phase{}, invalid(path+".agent", "is not allowed when mode is %q", ModeFanout)
		}
		if len(phase.Branches) < MinBranches || len(phase.Branches) > MaxBranches {
			return Phase{}, invalid(path+".branches", "must contain between %d and %d branches, got %d",
				MinBranches, MaxBranches, len(phase.Branches))
		}
		if phase.Join == nil {
			return Phase{}, invalid(path+".join", "is required when mode is %q", ModeFanout)
		}
		branchNames := make(map[string]int, len(phase.Branches))
		for bi, branch := range phase.Branches {
			branchPath := fmt.Sprintf("%s.branches[%d]", path, bi)
			if err := checkName(branchPath+".name", branch.Name, MaxPhaseNameLength); err != nil {
				return Phase{}, err
			}
			if previous, exists := branchNames[branch.Name]; exists {
				return Phase{}, invalid(branchPath+".name",
					"duplicates %s.branches[%d].name %q; branch names must be unique within a phase",
					path, previous, branch.Name)
			}
			branchNames[branch.Name] = bi
			if err := validateAgent(branchPath+".agent", branch.Agent); err != nil {
				return Phase{}, err
			}
			if branch.Budget != nil {
				if err := branch.Budget.Validate(); err != nil {
					return Phase{}, invalid(branchPath+".budget", "%v", err)
				}
			}
			if branch.Timeout != nil && branch.Timeout.Duration() <= 0 {
				return Phase{}, invalid(branchPath+".timeout", "must be positive")
			}
		}
		if err := validateJoin(path+".join", *phase.Join, len(phase.Branches)); err != nil {
			return Phase{}, err
		}
	default:
		return Phase{}, invalid(path+".mode", "must be %q or %q, got %q", ModeSingle, ModeFanout, phase.Mode)
	}

	dependsOn, err := resolveDependsOn(path, index, phase, def, seen)
	if err != nil {
		return Phase{}, err
	}

	if phase.When != nil {
		whenPath := path + ".when"
		if phase.When.AfterPhase == "" {
			return Phase{}, invalid(whenPath+".after_phase", "is required")
		}
		priorIndex, exists := seen[phase.When.AfterPhase]
		if !exists {
			return Phase{}, invalid(whenPath+".after_phase", "names %q, which is not a phase of this definition",
				phase.When.AfterPhase)
		}
		if priorIndex >= index {
			return Phase{}, invalid(whenPath+".after_phase",
				"names %q, which is not declared before this phase", phase.When.AfterPhase)
		}
		switch phase.When.Outcome {
		case OutcomeSucceeded, OutcomeFailed, OutcomeAny:
		default:
			return Phase{}, invalid(whenPath+".outcome", "must be %q, %q, or %q, got %q",
				OutcomeSucceeded, OutcomeFailed, OutcomeAny, phase.When.Outcome)
		}
	}

	retry, err := resolveRetry(path, phase.Retry, def.Defaults)
	if err != nil {
		return Phase{}, err
	}

	timeout := phase.Timeout
	if timeout == nil && def.Defaults != nil {
		timeout = def.Defaults.Timeout
	}
	if timeout != nil && timeout.Duration() <= 0 {
		return Phase{}, invalid(path+".timeout", "must be positive")
	}

	budget := phase.Budget
	if budget == nil && def.Defaults != nil {
		budget = def.Defaults.Budget
	}
	if budget != nil {
		if err := budget.Validate(); err != nil {
			return Phase{}, invalid(path+".budget", "%v", err)
		}
	}

	onFailure := phase.OnFailure
	if onFailure == "" && def.Defaults != nil {
		onFailure = def.Defaults.OnFailure
	}
	if onFailure == "" {
		onFailure = FailAbort
	}
	switch onFailure {
	case FailAbort, FailContinue, FailEscalate:
	default:
		return Phase{}, invalid(path+".on_failure", "must be %q, %q, or %q, got %q",
			FailAbort, FailContinue, FailEscalate, onFailure)
	}

	outputs := make([]string, len(phase.Outputs))
	seenOutputs := make(map[string]bool, len(phase.Outputs))
	for oi, kind := range phase.Outputs {
		outputPath := fmt.Sprintf("%s.outputs[%d]", path, oi)
		if !outputKindPattern.MatchString(kind) {
			return Phase{}, invalid(outputPath,
				"%q is not an artifact kind; accepted values are plan, diff, report, evidence, aggregate, log, custom:<name>", kind)
		}
		if seenOutputs[kind] {
			return Phase{}, invalid(outputPath, "duplicates %q; outputs must be unique", kind)
		}
		seenOutputs[kind] = true
		outputs[oi] = kind
	}

	resolved := phase
	resolved.Retry = retry
	resolved.Timeout = timeout
	resolved.Budget = budget
	resolved.OnFailure = onFailure
	resolved.DependsOn = dependsOn
	resolved.Outputs = outputs
	return resolved, nil
}

func resolveDependsOn(path string, index int, phase Phase, def Definition, seen map[string]int) ([]string, error) {
	if len(phase.DependsOn) == 0 {
		if index == 0 {
			// The first phase depends on nothing; scheduling starts with it.
			return nil, nil
		}
		return []string{def.Phases[index-1].Name}, nil
	}

	out := make([]string, len(phase.DependsOn))
	checked := make(map[string]bool, len(phase.DependsOn))
	for di, dependency := range phase.DependsOn {
		depPath := fmt.Sprintf("%s.depends_on[%d]", path, di)
		if dependency == "" {
			return nil, invalid(depPath, "must name a phase")
		}
		if checked[dependency] {
			return nil, invalid(depPath, "duplicates %q; depends_on entries must be unique", dependency)
		}
		checked[dependency] = true
		priorIndex, exists := seen[dependency]
		if !exists {
			return nil, invalid(depPath, "names %q, which is not a phase of this definition", dependency)
		}
		// Requiring an earlier declaration makes cycles impossible and keeps
		// execution order equal to declaration order, which the audit relies on.
		if priorIndex >= index {
			return nil, invalid(depPath, "names %q, which is not declared before this phase", dependency)
		}
		out[di] = dependency
	}
	return out, nil
}

func resolveRetry(path string, retry *Retry, defaults *PhaseDefaults) (*Retry, error) {
	source := retry
	if source == nil && defaults != nil {
		source = defaults.Retry
	}
	if source == nil {
		return &Retry{
			MaxAttempts: DefaultMaxAttempts,
			Backoff: &Backoff{
				Initial:    durationPtr(time.Duration(DefaultBackoffInitialMS) * time.Millisecond),
				Max:        durationPtr(time.Duration(DefaultBackoffMaxMS) * time.Millisecond),
				Multiplier: floatPtr(DefaultBackoffFactor),
			},
			RetryOn: []domain.ErrorClass{domain.ErrClassTransient, domain.ErrClassRateLimited},
		}, nil
	}

	retryPath := path + ".retry"
	if source.MaxAttempts == 0 {
		return nil, invalid(retryPath+".max_attempts", "is required when retry is set and must be >= 1")
	}
	if source.MaxAttempts < 1 || source.MaxAttempts > MaxRetryAttempts {
		return nil, invalid(retryPath+".max_attempts", "must be between 1 and %d, got %d",
			MaxRetryAttempts, source.MaxAttempts)
	}

	resolved := &Retry{
		MaxAttempts: source.MaxAttempts,
		Backoff: &Backoff{
			Initial:    durationPtr(time.Duration(DefaultBackoffInitialMS) * time.Millisecond),
			Max:        durationPtr(time.Duration(DefaultBackoffMaxMS) * time.Millisecond),
			Multiplier: floatPtr(DefaultBackoffFactor),
		},
		RetryOn: source.RetryOn,
	}
	if len(resolved.RetryOn) == 0 {
		resolved.RetryOn = []domain.ErrorClass{domain.ErrClassTransient, domain.ErrClassRateLimited}
	}
	for ri, class := range resolved.RetryOn {
		if !class.Valid() {
			return nil, invalid(fmt.Sprintf("%s.retry_on[%d]", path, ri),
				"%q is not an error class", class)
		}
	}

	if source.Backoff != nil {
		if source.Backoff.Initial != nil {
			if source.Backoff.Initial.Duration() <= 0 {
				return nil, invalid(retryPath+".backoff.initial", "must be positive")
			}
			resolved.Backoff.Initial = source.Backoff.Initial
		}
		if source.Backoff.Max != nil {
			if source.Backoff.Max.Duration() <= 0 {
				return nil, invalid(retryPath+".backoff.max", "must be positive")
			}
			resolved.Backoff.Max = source.Backoff.Max
		}
		if source.Backoff.Multiplier != nil {
			multiplier := *source.Backoff.Multiplier
			if math.IsNaN(multiplier) || multiplier < MinBackoffMultiplier || multiplier > MaxBackoffMultiplier {
				return nil, invalid(retryPath+".backoff.multiplier", "must be between %g and %g, got %g",
					MinBackoffMultiplier, MaxBackoffMultiplier, multiplier)
			}
			resolved.Backoff.Multiplier = source.Backoff.Multiplier
		}
	}
	return resolved, nil
}

func validateDefaults(defaults PhaseDefaults) error {
	if defaults.Retry != nil {
		if _, err := resolveRetry("defaults", defaults.Retry, nil); err != nil {
			return err
		}
	}
	if defaults.Timeout != nil && defaults.Timeout.Duration() <= 0 {
		return invalid("defaults.timeout", "must be positive")
	}
	if defaults.Budget != nil {
		if err := defaults.Budget.Validate(); err != nil {
			return invalid("defaults.budget", "%v", err)
		}
	}
	switch defaults.OnFailure {
	case "", FailAbort, FailContinue, FailEscalate:
	default:
		return invalid("defaults.on_failure", "must be %q, %q, or %q, got %q",
			FailAbort, FailContinue, FailEscalate, defaults.OnFailure)
	}
	return nil
}

func validateAgent(path string, agent AgentRef) error {
	if err := checkName(path+".name", agent.Name, MaxPhaseNameLength); err != nil {
		return err
	}
	if strings.TrimSpace(agent.Instructions) == "" {
		return invalid(path+".instructions", "is required")
	}
	if len(agent.Instructions) > MaxInstructionsLength {
		return invalid(path+".instructions", "must be at most %d characters, got %d",
			MaxInstructionsLength, len(agent.Instructions))
	}
	modelPath := path + ".model"
	if agent.Model.Provider != "openai-compatible" {
		return invalid(modelPath+".provider", "must be %q, got %q", "openai-compatible", agent.Model.Provider)
	}
	if agent.Model.Name == "" {
		return invalid(modelPath+".name", "is required")
	}
	if agent.Model.BaseURL != "" {
		parsed, err := url.Parse(agent.Model.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return invalid(modelPath+".base_url", "must be an absolute URI, got %q", agent.Model.BaseURL)
		}
	}
	if agent.Model.MaxOutputTokens < 0 {
		return invalid(modelPath+".max_output_tokens", "must be >= 1 when set, got %d", agent.Model.MaxOutputTokens)
	}
	if agent.Model.Temperature != nil {
		temperature := *agent.Model.Temperature
		if math.IsNaN(temperature) || temperature < 0 || temperature > 2 {
			return invalid(modelPath+".temperature", "must be between 0 and 2, got %g", temperature)
		}
	}
	if agent.Budget != nil {
		if err := agent.Budget.Validate(); err != nil {
			return invalid(path+".budget", "%v", err)
		}
	}
	return nil
}

func validateJoin(path string, join JoinPolicy, branchCount int) error {
	switch join.Rule {
	case JoinAll, JoinAny:
		if join.N != nil {
			return invalid(path+".n", "is only allowed when rule is %q", JoinNofM)
		}
	case JoinNofM:
		if join.N == nil {
			return invalid(path+".n", "is required when rule is %q", JoinNofM)
		}
		if *join.N < 2 || *join.N > branchCount {
			return invalid(path+".n", "must be between 2 and the branch count (%d), got %d", branchCount, *join.N)
		}
	default:
		return invalid(path+".rule", "must be %q, %q, or %q, got %q", JoinAll, JoinAny, JoinNofM, join.Rule)
	}
	return nil
}

func checkName(path, name string, maxLength int) error {
	if name == "" {
		return invalid(path, "is required")
	}
	if len(name) > maxLength {
		return invalid(path, "must be at most %d characters, got %d", maxLength, len(name))
	}
	if !namePattern.MatchString(name) {
		return invalid(path, "must match %s, got %q", namePattern.String(), name)
	}
	return nil
}

// invalid builds the classified, path-carrying error every validation failure
// returns. The hint starts with the offending field so an operator can fix the
// file without reading the schema (FR-052).
func invalid(path, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "workflow definition is invalid",
		Hint:  path + ": " + message,
		Err:   domain.ErrInvalidDefinition,
	}
}

func durationPtr(d time.Duration) *domain.Duration {
	value := domain.NewDuration(d)
	return &value
}

func floatPtr(value float64) *float64 { return &value }
