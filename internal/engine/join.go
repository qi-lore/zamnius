package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// evaluateJoin applies the declared rule to the branch outcomes **in declared
// order**. Completion order is never used, which is what makes the aggregate
// artifact byte-identical across runs (FR-003).
func evaluateJoin(policy *definition.JoinPolicy, branches []definition.Branch, outcomes []phaseOutcome) domain.JoinResult {
	result := domain.JoinResult{Rule: "all"}
	if policy != nil {
		result.Rule = string(policy.Rule)
	}
	if policy != nil && policy.N != nil {
		result.N = *policy.N
	}

	successful := 0
	for i, branch := range branches {
		outcome := outcomes[i]
		branchOutcome := domain.BranchOutcome{
			Branch:      branch.Name,
			AttemptNo:   outcome.Attempts,
			ErrorClass:  outcome.ErrorClass,
			ErrorDetail: outcome.FailureReason,
			Artifacts:   outcome.Artifacts,
		}
		switch outcome.Status {
		case domain.PhaseSucceeded:
			branchOutcome.Status = domain.AttemptSucceeded
			successful++
		case domain.PhaseCancelled:
			branchOutcome.Status = domain.AttemptAbandoned
		default:
			branchOutcome.Status = domain.AttemptFailed
		}
		result.Branches = append(result.Branches, branchOutcome)
	}

	switch result.Rule {
	case string(definition.JoinAny):
		result.Satisfied = successful > 0
	case string(definition.JoinNofM):
		result.Satisfied = successful >= result.N
	default:
		result.Satisfied = successful == len(branches)
	}
	return result
}

// persistAggregate writes the deterministic join summary as one `aggregate`
// artifact. Its bytes are canonical JSON of the declared-order branch outcomes, so
// two runs of the same workflow produce the same digest.
func (s *session) persistAggregate(ctx context.Context, phase definition.Phase, join domain.JoinResult) (*domain.ArtifactRef, error) {
	summary := struct {
		Phase     string                 `json:"phase"`
		Rule      string                 `json:"rule"`
		N         int                    `json:"n,omitempty"`
		Satisfied bool                   `json:"satisfied"`
		Branches  []domain.BranchOutcome `json:"branches"`
	}{
		Phase:     phase.Name,
		Rule:      join.Rule,
		N:         join.N,
		Satisfied: join.Satisfied,
		Branches:  join.Branches,
	}
	content, err := domain.CanonicalJSON(summary)
	if err != nil {
		return nil, fmt.Errorf("encode aggregate for %s: %w", phase.Name, err)
	}

	ref, err := s.engine.artifacts.Put(ctx, s.run.ID, "aggregate", "application/json", strings.NewReader(string(content)))
	if err != nil {
		return nil, fmt.Errorf("write aggregate for %s: %w", phase.Name, err)
	}
	ref.Phase = phase.Name
	return &ref, nil
}
