package engine

import (
	"fmt"
	"time"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// retryDecision decides whether another attempt is allowed, and explains why in
// words an auditor can read. The rationale names the triggering condition, which
// is what makes the retry decision reconstructable (SC-005, US3 scenario 2).
func (s *session) retryDecision(phase definition.Phase, attemptNo int, class domain.ErrorClass) (bool, string) {
	if phase.Retry == nil {
		return false, ""
	}
	if attemptNo >= phase.Retry.MaxAttempts {
		return false, fmt.Sprintf("attempt %d of %d is the last allowed by max_attempts", attemptNo, phase.Retry.MaxAttempts)
	}
	for _, retryable := range phase.Retry.RetryOn {
		if retryable == class {
			return true, fmt.Sprintf("error class %q is listed in retry_on and attempt %d of %d has been used",
				class, attemptNo, phase.Retry.MaxAttempts)
		}
	}
	return false, fmt.Sprintf("error class %q is not listed in retry_on (%v)", class, phase.Retry.RetryOn)
}

// backoffFor computes the wait before a given attempt number: initial scaled by
// multiplier^(n-1), bounded by max. The bound is applied after scaling so a large
// multiplier cannot produce an unbounded sleep.
func (s *session) backoffFor(phase definition.Phase, nextAttempt int) time.Duration {
	if phase.Retry == nil || phase.Retry.Backoff == nil {
		return 0
	}
	backoff := phase.Retry.Backoff
	if backoff.Initial == nil {
		return 0
	}
	wait := backoff.Initial.Duration()
	multiplier := 1.0
	if backoff.Multiplier != nil {
		multiplier = *backoff.Multiplier
	}
	for i := 1; i < nextAttempt-1; i++ {
		wait = time.Duration(float64(wait) * multiplier)
	}
	if backoff.Max != nil && wait > backoff.Max.Duration() {
		wait = backoff.Max.Duration()
	}
	if wait < 0 {
		return 0
	}
	return wait
}
