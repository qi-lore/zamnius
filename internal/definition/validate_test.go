package definition_test

import (
	"strings"
	"testing"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// TestNormalizeRejectsInvalidDefinitions is the SC-012 discipline applied to
// definitions: every case must name the offending field path, so an operator can
// fix the file without reading the schema.
func TestNormalizeRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	validAgent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	otherAgent := definition.Agent("coder", "write code", "gpt-4.1-mini", 512)

	longName := strings.Repeat("a", 121)
	longPhaseName := strings.Repeat("p", 81)
	longInstructions := strings.Repeat("i", 20001)
	longDescription := strings.Repeat("d", 2001)

	// base builds a fresh agent per case: several cases mutate the agent through
	// its pointer, and subtests run in parallel, so a shared value would leak
	// mutations between cases.
	base := func() definition.Definition {
		agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
		return definition.Definition{
			Version: "1",
			Name:    "base",
			Phases:  []definition.Phase{{Name: "plan", Mode: definition.ModeSingle, Agent: &agent}},
		}
	}

	tests := []struct {
		name     string
		mutate   func(*definition.Definition)
		wantPath string
	}{
		{"missing version", func(d *definition.Definition) { d.Version = "" }, "version"},
		{"wrong version", func(d *definition.Definition) { d.Version = "2" }, "version"},
		{"missing name", func(d *definition.Definition) { d.Name = "" }, "name"},
		{"name with uppercase", func(d *definition.Definition) { d.Name = "My-Flow" }, "name"},
		{"name too long", func(d *definition.Definition) { d.Name = longName }, "name"},
		{"description too long", func(d *definition.Definition) { d.Description = longDescription }, "description"},
		{"empty budget", func(d *definition.Definition) { d.Budget = &domain.Budget{} }, "budget"},
		{"budget attempts zero", func(d *definition.Definition) { d.Budget = attemptsBudget(0) }, "budget"},
		{"budget tokens zero", func(d *definition.Definition) { d.Budget = tokensBudget(0) }, "budget"},
		{"no phases", func(d *definition.Definition) { d.Phases = nil }, "phases"},
		{"missing phase name", func(d *definition.Definition) { d.Phases[0].Name = "" }, "phases[0].name"},
		{"phase name uppercase", func(d *definition.Definition) { d.Phases[0].Name = "Plan" }, "phases[0].name"},
		{"phase name too long", func(d *definition.Definition) { d.Phases[0].Name = longPhaseName }, "phases[0].name"},
		{"unknown mode", func(d *definition.Definition) { d.Phases[0].Mode = "parallel" }, "phases[0].mode"},
		{"single without agent", func(d *definition.Definition) { d.Phases[0].Agent = nil }, "phases[0].agent"},
		{"single with branches", func(d *definition.Definition) {
			d.Phases[0].Branches = []definition.Branch{definition.BranchOf("a", validAgent), definition.BranchOf("b", otherAgent)}
		}, "phases[0].branches"},
		{"single with join", func(d *definition.Definition) {
			d.Phases[0].Join = &definition.JoinPolicy{Rule: definition.JoinAll}
		}, "phases[0].join"},
		{"duplicate phase name", func(d *definition.Definition) {
			d.Phases = append(d.Phases, definition.Phase{Name: "plan", Mode: definition.ModeSingle, Agent: &otherAgent})
		}, "phases[1].name"},
		{"fanout with agent", func(d *definition.Definition) {
			d.Phases[0] = fanoutPhase("fan", validAgent, otherAgent)
			d.Phases[0].Agent = &validAgent
		}, "phases[0].agent"},
		{"fanout with one branch", func(d *definition.Definition) {
			d.Phases[0] = definition.Phase{
				Name: "fan", Mode: definition.ModeFanout,
				Branches: []definition.Branch{definition.BranchOf("a", validAgent)},
				Join:     &definition.JoinPolicy{Rule: definition.JoinAll},
			}
		}, "phases[0].branches"},
		{"fanout without join", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = nil
			d.Phases[0] = phase
		}, "phases[0].join"},
		{"duplicate branch name", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Branches[1].Name = phase.Branches[0].Name
			d.Phases[0] = phase
		}, "phases[0].branches[1].name"},
		{"branch name uppercase", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Branches[0].Name = "Branch-One"
			d.Phases[0] = phase
		}, "phases[0].branches[0].name"},
		{"join rule unknown", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = &definition.JoinPolicy{Rule: "most"}
			d.Phases[0] = phase
		}, "phases[0].join.rule"},
		{"n_of_m without n", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = &definition.JoinPolicy{Rule: definition.JoinNofM}
			d.Phases[0] = phase
		}, "phases[0].join.n"},
		{"n_of_m below two", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = &definition.JoinPolicy{Rule: definition.JoinNofM, N: intPtr(1)}
			d.Phases[0] = phase
		}, "phases[0].join.n"},
		{"n_of_m above branch count", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = &definition.JoinPolicy{Rule: definition.JoinNofM, N: intPtr(3)}
			d.Phases[0] = phase
		}, "phases[0].join.n"},
		{"n with all rule", func(d *definition.Definition) {
			phase := fanoutPhase("fan", validAgent, otherAgent)
			phase.Join = &definition.JoinPolicy{Rule: definition.JoinAll, N: intPtr(2)}
			d.Phases[0] = phase
		}, "phases[0].join.n"},
		{"depends_on unknown phase", func(d *definition.Definition) { d.Phases[0].DependsOn = []string{"ghost"} }, "phases[0].depends_on[0]"},
		{"depends_on forward reference", func(d *definition.Definition) {
			d.Phases = []definition.Phase{
				{Name: "first", Mode: definition.ModeSingle, Agent: &validAgent, DependsOn: []string{"second"}},
				{Name: "second", Mode: definition.ModeSingle, Agent: &otherAgent},
			}
		}, "phases[0].depends_on[0]"},
		{"depends_on self reference", func(d *definition.Definition) {
			d.Phases[0].DependsOn = []string{"plan"}
		}, "phases[0].depends_on[0]"},
		{"depends_on duplicate", func(d *definition.Definition) {
			d.Phases = []definition.Phase{
				{Name: "one", Mode: definition.ModeSingle, Agent: &validAgent},
				{Name: "two", Mode: definition.ModeSingle, Agent: &otherAgent, DependsOn: []string{"one", "one"}},
			}
		}, "phases[1].depends_on[1]"},
		{"when unknown phase", func(d *definition.Definition) {
			d.Phases[0].When = &definition.Condition{AfterPhase: "ghost", Outcome: definition.OutcomeAny}
		}, "phases[0].when.after_phase"},
		{"when missing after_phase", func(d *definition.Definition) {
			d.Phases[0].When = &definition.Condition{Outcome: definition.OutcomeAny}
		}, "phases[0].when.after_phase"},
		{"when outcome unknown", func(d *definition.Definition) {
			d.Phases = []definition.Phase{
				{Name: "one", Mode: definition.ModeSingle, Agent: &validAgent},
				{Name: "two", Mode: definition.ModeSingle, Agent: &otherAgent,
					When: &definition.Condition{AfterPhase: "one", Outcome: "maybe"}},
			}
		}, "phases[1].when.outcome"},
		{"on_failure unknown", func(d *definition.Definition) { d.Phases[0].OnFailure = "retry-forever" }, "phases[0].on_failure"},
		{"retry without max_attempts", func(d *definition.Definition) {
			d.Phases[0].Retry = &definition.Retry{}
		}, "phases[0].retry.max_attempts"},
		{"retry above maximum", func(d *definition.Definition) {
			d.Phases[0].Retry = &definition.Retry{MaxAttempts: 21}
		}, "phases[0].retry.max_attempts"},
		{"retry_on unknown class", func(d *definition.Definition) {
			d.Phases[0].Retry = &definition.Retry{MaxAttempts: 2, RetryOn: []domain.ErrorClass{"cosmic"}}
		}, "phases[0].retry_on[0]"},
		{"backoff multiplier too large", func(d *definition.Definition) {
			d.Phases[0].Retry = &definition.Retry{MaxAttempts: 2, Backoff: &definition.Backoff{Multiplier: floatPtr(11)}}
		}, "phases[0].retry.backoff.multiplier"},
		{"backoff multiplier below one", func(d *definition.Definition) {
			d.Phases[0].Retry = &definition.Retry{MaxAttempts: 2, Backoff: &definition.Backoff{Multiplier: floatPtr(0.5)}}
		}, "phases[0].retry.backoff.multiplier"},
		{"agent name uppercase", func(d *definition.Definition) { d.Phases[0].Agent.Name = "Planner" }, "phases[0].agent.name"},
		{"agent instructions empty", func(d *definition.Definition) { d.Phases[0].Agent.Instructions = "   " }, "phases[0].agent.instructions"},
		{"agent instructions too long", func(d *definition.Definition) {
			d.Phases[0].Agent.Instructions = longInstructions
		}, "phases[0].agent.instructions"},
		{"model provider unknown", func(d *definition.Definition) { d.Phases[0].Agent.Model.Provider = "anthropic" }, "phases[0].agent.model.provider"},
		{"model name empty", func(d *definition.Definition) { d.Phases[0].Agent.Model.Name = "" }, "phases[0].agent.model.name"},
		{"model base_url relative", func(d *definition.Definition) {
			d.Phases[0].Agent.Model.BaseURL = "/v1"
		}, "phases[0].agent.model.base_url"},
		{"model max_output_tokens negative", func(d *definition.Definition) {
			d.Phases[0].Agent.Model.MaxOutputTokens = -1
		}, "phases[0].agent.model.max_output_tokens"},
		{"model temperature too high", func(d *definition.Definition) {
			d.Phases[0].Agent.Model.Temperature = floatPtr(2.5)
		}, "phases[0].agent.model.temperature"},
		{"agent budget empty", func(d *definition.Definition) { d.Phases[0].Agent.Budget = &domain.Budget{} }, "phases[0].agent.budget"},
		{"outputs unknown kind", func(d *definition.Definition) { d.Phases[0].Outputs = []string{"screenshot"} }, "phases[0].outputs[0]"},
		{"outputs duplicate", func(d *definition.Definition) { d.Phases[0].Outputs = []string{"plan", "plan"} }, "phases[0].outputs[1]"},
		{"defaults on_failure unknown", func(d *definition.Definition) {
			d.Defaults = &definition.PhaseDefaults{OnFailure: "ignore"}
		}, "defaults.on_failure"},
		{"defaults budget empty", func(d *definition.Definition) {
			d.Defaults = &definition.PhaseDefaults{Budget: &domain.Budget{}}
		}, "defaults.budget"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			def := base()
			tc.mutate(&def)

			_, err := definition.Normalize(def)
			if err == nil {
				t.Fatalf("Normalize accepted an invalid definition")
			}
			hint := domain.HintOf(err)
			if !strings.HasPrefix(hint, tc.wantPath+":") {
				t.Fatalf("hint = %q, want it to name %q", hint, tc.wantPath)
			}
			if !strings.Contains(hint, tc.wantPath) {
				t.Fatalf("hint lost the field path: %q", hint)
			}
			if got := domain.ClassOf(err); got != domain.ErrClassPermanent {
				t.Errorf("class = %q, want permanent", got)
			}
		})
	}
}

func TestNormalizeAppliesDefaults(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	def, err := definition.Normalize(definition.Definition{
		Version: "1",
		Name:    "defaults",
		Phases: []definition.Phase{
			{Name: "plan", Mode: definition.ModeSingle, Agent: &agent},
			{Name: "build", Mode: definition.ModeSingle, Agent: &agent},
		},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	first := def.Phases[0]
	// Retry defaults are resolved so the engine never guesses.
	if first.Retry == nil || first.Retry.MaxAttempts != definition.DefaultMaxAttempts {
		t.Fatalf("retry not defaulted: %+v", first.Retry)
	}
	if first.Retry.Backoff == nil || first.Retry.Backoff.Initial.String() != "1s" ||
		first.Retry.Backoff.Max.String() != "30s" || *first.Retry.Backoff.Multiplier != 2 {
		t.Fatalf("backoff not defaulted: %+v", first.Retry.Backoff)
	}
	if len(first.Retry.RetryOn) != 2 || first.Retry.RetryOn[0] != domain.ErrClassTransient {
		t.Fatalf("retry_on not defaulted: %v", first.Retry.RetryOn)
	}
	if first.OnFailure != definition.FailAbort {
		t.Errorf("on_failure = %q, want abort", first.OnFailure)
	}
	if len(first.DependsOn) != 0 {
		t.Errorf("first phase depends_on = %v, want none", first.DependsOn)
	}
	// depends_on defaults to the previous phase, which is what makes execution
	// order and declared order the same thing.
	if len(def.Phases[1].DependsOn) != 1 || def.Phases[1].DependsOn[0] != "plan" {
		t.Errorf("second phase depends_on = %v, want [plan]", def.Phases[1].DependsOn)
	}
}

func TestNormalizeInheritsPhaseDefaults(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	timeout := domain.NewDuration(90 * 1000 * 1000 * 1000) // 90s, kept single-unit
	def, err := definition.Normalize(definition.Definition{
		Version: "1",
		Name:    "inherits",
		Defaults: &definition.PhaseDefaults{
			Retry:     &definition.Retry{MaxAttempts: 3},
			Timeout:   &timeout,
			OnFailure: definition.FailContinue,
		},
		Phases: []definition.Phase{{Name: "plan", Mode: definition.ModeSingle, Agent: &agent}},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	phase := def.Phases[0]
	if phase.Retry.MaxAttempts != 3 {
		t.Errorf("max_attempts = %d, want 3", phase.Retry.MaxAttempts)
	}
	if phase.Timeout == nil || phase.Timeout.String() != "90s" {
		t.Errorf("timeout = %v, want 90s", phase.Timeout)
	}
	if phase.OnFailure != definition.FailContinue {
		t.Errorf("on_failure = %q, want continue", phase.OnFailure)
	}
}

func TestBuilderAndDecoderAgree(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	viaBuilder, err := definition.NewBuilder("parity").
		Describe("built in code").
		WithPhase("plan", agent, definition.WithOutputs("plan")).
		Build()
	if err != nil {
		t.Fatalf("builder Build() error = %v", err)
	}

	document := []byte(`
version: "1"
name: parity
description: built in code
phases:
  - name: plan
    mode: single
    agent:
      name: planner
      instructions: make a plan
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 512
    outputs: [plan]
`)
	viaFile, err := definition.Decode(document, definition.FormatYAML)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	left, err := definition.Fingerprint(viaBuilder)
	if err != nil {
		t.Fatalf("Fingerprint(builder) error = %v", err)
	}
	right, err := definition.Fingerprint(viaFile)
	if err != nil {
		t.Fatalf("Fingerprint(file) error = %v", err)
	}
	if left != right {
		t.Fatalf("builder and file produced different fingerprints:\n%s\n%s", left, right)
	}
}

func fanoutPhase(name string, agents ...definition.AgentRef) definition.Phase {
	branches := make([]definition.Branch, 0, len(agents))
	for _, agent := range agents {
		branches = append(branches, definition.BranchOf(agent.Name, agent))
	}
	return definition.Phase{
		Name:     name,
		Mode:     definition.ModeFanout,
		Branches: branches,
		Join:     &definition.JoinPolicy{Rule: definition.JoinAll},
	}
}

func attemptsBudget(attempts int) *domain.Budget { return &domain.Budget{Attempts: &attempts} }

func tokensBudget(tokens int) *domain.Budget { return &domain.Budget{Tokens: &tokens} }

func intPtr(value int) *int { return &value }

func floatPtr(value float64) *float64 { return &value }
