package engine

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
)

// runFanout executes every branch of a fan-out phase concurrently, bounded by the
// configured concurrency, then joins the results. A branch failure is data, not an
// error: the join rule is the only thing allowed to decide what it means.
func (s *session) runFanout(ctx context.Context, index int, phase definition.Phase) (phaseOutcome, error) {
	state, err := s.phaseState(ctx, phase.Name)
	if err != nil {
		return phaseOutcome{}, err
	}
	if err := s.startPhase(ctx, &state, phase.Name); err != nil {
		return phaseOutcome{}, err
	}

	outcomes := make([]phaseOutcome, len(phase.Branches))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(s.maxConcurrency)

	for i := range phase.Branches {
		branch := phase.Branches[i]
		// A branch whose attempt already succeeded is reused, not recomputed: the
		// resumed run executes only the unfinished branches and the join completes
		// with the full set of results (FR-013, SC-002, US2 scenario 1).
		if recovered, ok := s.recoveredBranch(phase.Name, branch.Agent.Name); ok {
			outcomes[i] = phaseOutcome{
				Status:    domain.PhaseSucceeded,
				Attempts:  recovered.attemptNo,
				Artifacts: recovered.artifacts,
			}
			continue
		}
		group.Go(func() error {
			// Branches never abort their siblings; they return nil so errgroup
			// waits for all of them even when one fails.
			result := s.executeAttempts(groupCtx, index, phase, branch.Agent,
				branchTimeout(branch, phase), budgetFor(phase, branch.Budget), branch.Name)
			// Each goroutine writes its own index of a slice that is never
			// reallocated, so these writes do not race and need no lock.
			outcomes[i] = result
			return nil
		})
	}
	_ = group.Wait()

	total := 0
	artifacts := make([]domain.ArtifactRef, 0)
	cancelled := false
	for _, outcome := range outcomes {
		total += outcome.Attempts
		artifacts = append(artifacts, outcome.Artifacts...)
		if outcome.Status == domain.PhaseCancelled {
			cancelled = true
		}
	}
	if ctx.Err() != nil || cancelled {
		return phaseOutcome{
			Status:        domain.PhaseCancelled,
			Attempts:      total,
			ErrorClass:    domain.ErrClassCancelled,
			FailureReason: "cancelled during a parallel branch",
		}, nil
	}

	join := evaluateJoin(phase.Join, phase.Branches, outcomes)
	result := phaseOutcome{
		Status:    domain.PhaseSucceeded,
		Attempts:  total,
		Artifacts: artifacts,
		Join:      &join,
	}
	if !join.Satisfied {
		result.Status = domain.PhaseFailed
		result.ErrorClass = domain.ErrClassPermanent
		result.FailureReason = joinFailureReason(phase.Name, join)
	}

	if join.Satisfied && len(phase.Outputs) > 0 {
		// The aggregate exists only for a satisfied join: a failed `all` join must
		// not leave a partial aggregate behind (US1 scenario 5).
		ref, err := s.persistAggregate(ctx, phase, join)
		if err != nil {
			result.Status = domain.PhaseFailed
			result.ErrorClass = domain.ErrClassInternal
			result.FailureReason = causeOf(err)
		} else if ref != nil {
			result.Artifacts = append(result.Artifacts, *ref)
			s.recordArtifacts(phase.Name, []domain.ArtifactRef{*ref})
		}
	}

	if err := s.finishPhase(ctx, phase, &state, result, ""); err != nil {
		return phaseOutcome{}, err
	}
	return result, nil
}

// branchTimeout prefers the branch's own timeout, falling back to the phase's.
func branchTimeout(branch definition.Branch, phase definition.Phase) *domain.Duration {
	if branch.Timeout != nil {
		return branch.Timeout
	}
	return phase.Timeout
}

func joinFailureReason(phase string, join domain.JoinResult) string {
	failed := make([]string, 0, len(join.Branches))
	for _, branch := range join.Branches {
		if branch.Status != domain.AttemptSucceeded {
			failed = append(failed, branch.Branch)
		}
	}
	return "join " + join.Rule + " not satisfied for phase " + phase + "; failed branches: " + describeBranches(failed)
}

func describeBranches(branches []string) string {
	if len(branches) == 0 {
		return "none"
	}
	out := ""
	for i, branch := range branches {
		if i > 0 {
			out += ", "
		}
		out += branch
	}
	return out
}
