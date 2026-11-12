package engine

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/domain"
)

// renewLease extends ownership. A renewal failure means another process took the
// run, which must stop the current owner rather than let it keep writing
// (ADR-0004, FR-016).
func (e *Engine) renewLease(ctx context.Context, lease domain.Lease) (domain.Lease, error) {
	return e.store.RenewLease(ctx, lease, e.leaseTTL)
}

// startHeartbeat launches the renewal goroutine and returns the context the drive
// loop must use plus an idempotent stop. The returned context is cancelled when a
// renewal fails, so the loop stops and pauses with the recorded cause (T048).
func (s *session) startHeartbeat(ctx context.Context) (context.Context, func()) {
	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	s.stopHeartbeat = stop

	go func() {
		defer close(done)
		s.heartbeat(hbCtx, cancel)
	}()
	return hbCtx, stop
}

// heartbeat renews the lease every TTL/3 until the context ends. Two renewals may
// fail before the lease is genuinely at risk, so the cadence is a third of the TTL
// (ADR-0004). A renewal that fails for a reason other than shutdown means
// ownership was lost: the cause is recorded and the run context cancelled.
func (s *session) heartbeat(ctx context.Context, cancel context.CancelFunc) {
	interval := s.engine.leaseTTL / 3
	if interval <= 0 {
		interval = s.engine.leaseTTL
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A tick that races shutdown is not a lost lease: the context ending is
			// the caller stopping the run, not another owner taking it.
			if ctx.Err() != nil {
				return
			}
			renewed, err := s.engine.renewLease(ctx, s.currentLease())
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.recordLeaseLoss(err)
				cancel()
				return
			}
			s.setLease(renewed)
		}
	}
}

// currentLease returns the current ownership claim under the lease lock.
func (s *session) currentLease() domain.Lease {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	return s.lease
}

// setLease replaces the ownership claim under the lease lock.
func (s *session) setLease(lease domain.Lease) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	s.lease = lease
}

// recordLeaseLoss remembers the first renewal failure, which is the cause the run
// pauses with.
func (s *session) recordLeaseLoss(err error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.leaseLost == nil {
		s.leaseLost = err
	}
}

// leaseLossCause reports why ownership was lost, or nil if it was not.
func (s *session) leaseLossCause() error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	return s.leaseLost
}

// releaseLease gives up ownership. It stops the heartbeat first so a renewal can
// never race the release and resurrect a lease the run is dropping. Failure is
// logged and ignored: the lease will expire, and a release error must not mask the
// outcome the caller already has.
func (s *session) releaseLease(ctx context.Context) {
	if s.stopHeartbeat != nil {
		s.stopHeartbeat()
	}
	lease := s.currentLease()
	if lease.RunID == uuid.Nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.engine.store.ReleaseLease(releaseCtx, lease); err != nil {
		s.engine.logger.Warn("could not release lease", "run_id", s.run.ID.String(), "error", err.Error())
	}
}
