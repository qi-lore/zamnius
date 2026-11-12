package engine

import (
	"fmt"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Budget units, used verbatim in events and decisions so a report can name the
// allowance that ran out (FR-007, US1 scenario 8).
const (
	BudgetAttempts  = "attempts"
	BudgetWallClock = "wall_clock"
	BudgetTokens    = "tokens"
)

// budgetStop reports whether the next attempt is allowed and, when it is not,
// which allowance is exhausted. The phase budget is checked first because it is
// the narrower promise; the run budget is the outer bound.
func (s *session) budgetStop(budget *domain.Budget) (bool, string) {
	spent := s.spentNow()

	if budget != nil {
		if budget.Attempts != nil && spent.Attempts >= *budget.Attempts {
			return true, BudgetAttempts
		}
		if budget.Tokens != nil && spent.Tokens() >= *budget.Tokens {
			return true, BudgetTokens
		}
		if budget.WallClock != nil && spent.WallClockMS >= budget.WallClock.Duration().Milliseconds() {
			return true, BudgetWallClock
		}
	}

	runBudget := s.run.Budget
	if runBudget.Attempts != nil && spent.Attempts >= *runBudget.Attempts {
		return true, BudgetAttempts
	}
	if runBudget.Tokens != nil && spent.Tokens() >= *runBudget.Tokens {
		return true, BudgetTokens
	}
	if runBudget.WallClock != nil && spent.WallClockMS >= runBudget.WallClock.Duration().Milliseconds() {
		return true, BudgetWallClock
	}
	return false, ""
}

// stopForBudget ends the attempt loop without calling the model. Usage that
// already happened stays recorded; only the next attempt is refused
// (contracts/model-client.md §5).
func (s *session) stopForBudget(attempts int, unit string) phaseOutcome {
	return phaseOutcome{
		Status:        domain.PhaseFailed,
		Attempts:      attempts,
		ErrorClass:    domain.ErrClassBudgetExhausted,
		BudgetUnit:    unit,
		FailureReason: fmt.Sprintf("budget exhausted: the %s allowance was reached after %d attempt(s)", unit, attempts),
	}
}

// pendingSpend returns the consumption awaiting its committing transaction and
// resets the counter, so each phase commit carries exactly its own delta and two
// branches cannot erase each other's usage.
func (s *session) pendingSpend() domain.Consumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending
	s.pending = domain.Consumption{}
	return pending
}

// budgetFor resolves the effective budget of a phase: its own when declared,
// otherwise nothing extra (the run budget always applies on top).
func budgetFor(phase definition.Phase, fallback *domain.Budget) *domain.Budget {
	if phase.Budget != nil {
		return phase.Budget
	}
	return fallback
}
