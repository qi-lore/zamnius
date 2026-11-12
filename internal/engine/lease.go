package engine

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

// renewLease extends ownership. A renewal failure means another process took the
// run, which must stop the current owner rather than let it keep writing
// (ADR-0004, FR-016).
func (e *Engine) renewLease(ctx context.Context, lease domain.Lease) (domain.Lease, error) {
	return e.store.RenewLease(ctx, lease, e.leaseTTL)
}

// releaseLease gives up ownership. Failure is logged and ignored: the lease will
// expire, and a release error must not mask the outcome the caller already has.
func (s *session) releaseLease(ctx context.Context) {
	if s.lease.RunID == uuid.Nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.engine.store.ReleaseLease(releaseCtx, s.lease); err != nil {
		s.engine.logger.Warn("could not release lease", "run_id", s.run.ID.String(), "error", err.Error())
	}
}

// leaseTTLForReports is the renewal cadence: a third of the TTL, so two renewals
// may fail before the lease is genuinely at risk.
func (e *Engine) leaseTTLForReports() time.Duration { return e.leaseTTL / 3 }
