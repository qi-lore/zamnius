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
			Digests:     digestsOf(outcome.Artifacts),
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

// digestsOf reduces artifact references to their digests, which is all the join
// aggregate needs: the identity of a produced document without the run-specific
// rows that would make the aggregate differ between two runs.
func digestsOf(refs []domain.ArtifactRef) []string {
	if len(refs) == 0 {
		return nil
	}
	digests := make([]string, 0, len(refs))
	for _, ref := range refs {
		digests = append(digests, ref.SHA256)
	}
	return digests
}

// branchSummary is the aggregate document's view of one branch: its result, not the
// execution metadata around it. Attempt numbers are deliberately absent — a branch
// restarted after a crash has a different attempt number but the same result, and the
// aggregate must stay byte-identical across a resume (FR-003, quickstart scenario 2).
type branchSummary struct {
	Branch      string               `json:"branch"`
	Status      domain.AttemptStatus `json:"status"`
	ErrorClass  domain.ErrorClass    `json:"error_class,omitempty"`
	ErrorDetail string               `json:"error_detail,omitempty"`
	Digests     []string             `json:"digests,omitempty"`
}

// persistAggregate writes the deterministic join summary as one `aggregate`
// artifact. Its bytes are canonical JSON of the declared-order branch results, so
// two runs of the same workflow produce the same digest — including a run resumed
// after a crash.
func (s *session) persistAggregate(ctx context.Context, phase definition.Phase, join domain.JoinResult) (*domain.ArtifactRef, error) {
	summaries := make([]branchSummary, 0, len(join.Branches))
	for _, branch := range join.Branches {
		summaries = append(summaries, branchSummary{
			Branch:      branch.Branch,
			Status:      branch.Status,
			ErrorClass:  branch.ErrorClass,
			ErrorDetail: branch.ErrorDetail,
			Digests:     branch.Digests,
		})
	}

	summary := struct {
		Phase     string          `json:"phase"`
		Rule      string          `json:"rule"`
		N         int             `json:"n,omitempty"`
		Satisfied bool            `json:"satisfied"`
		Branches  []branchSummary `json:"branches"`
	}{
		Phase:     phase.Name,
		Rule:      join.Rule,
		N:         join.N,
		Satisfied: join.Satisfied,
		Branches:  summaries,
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
