package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/audit"
	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Resume continues an existing run. It never re-executes committed work: the
// definition comes from the stored snapshot, the lease is taken conditionally, the
// event chain is verified, and any attempt without a committed outcome is
// reconciled before execution continues (contracts/store.md §5).
func (e *Engine) Resume(ctx context.Context, runID uuid.UUID, opts ResumeOptions) (RunResult, error) {
	run, err := e.store.LoadRun(ctx, runID)
	if err != nil {
		return RunResult{RunID: runID, Err: err}, err
	}

	// Terminal runs are immutable; resuming one is a no-op that changes nothing
	// (FR-018, contracts/library-api.md §4.1).
	if run.Status.Terminal() {
		e.logger.Info("resume on a terminal run", "run_id", run.ID.String(), "status", string(run.Status))
		return RunResult{RunID: run.ID, Status: run.Status}, nil
	}

	if checker, ok := e.store.(interface {
		CheckRunVersion(ctx context.Context, run domain.Run) error
	}); ok {
		if err := checker.CheckRunVersion(ctx, run); err != nil {
			return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
		}
	}

	def, err := definition.FromSnapshot(run.DefinitionJSON)
	if err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}
	if err := e.checkDefinitionDrift(ctx, run, def, opts.AllowDefinitionDrift); err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	lease, err := e.store.AcquireLease(ctx, run.ID, e.owner, e.leaseTTL)
	if err != nil {
		// Someone else owns the run right now: the caller needs the live owner and
		// expiry to decide whether to wait (T051).
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	session := &session{
		engine:         e,
		run:            run,
		def:            def,
		lease:          lease,
		observer:       opts.Observer,
		maxConcurrency: clampConcurrency(opts.MaxConcurrency),
		kinds:          make(map[string]map[string]struct{}),
		artifacts:      make(map[string][]domain.ArtifactRef),
		recovered:      make(map[string]map[string]recoveredAttempt),
	}
	if err := session.refresh(ctx); err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}
	session.loadPhaseKinds()
	if err := session.seedAttemptNumbering(ctx); err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	events, err := e.store.Events(ctx, run.ID, 0)
	if err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}
	if problems := audit.VerifyChain(events); len(problems) > 0 {
		// A broken chain means the record cannot be trusted, so the run is failed
		// with the evidence rather than resumed on partial knowledge.
		return session.failOnIntegrity(ctx, "event chain verification failed", problems)
	}

	if err := session.reconcileAttempts(ctx, events); err != nil {
		return session.result(err), err
	}

	position := session.nextPhaseName()
	reason := fmt.Sprintf("resumed from status %s", run.Status)
	if err := session.commitRun(ctx, domain.RunDelta{Status: domain.RunRunning, CurrentPhase: position},
		[]domain.EventDraft{{
			Type:  domain.EventRunResumed,
			Actor: domain.ActorSystem,
			Payload: map[string]any{
				"reason":        reason,
				"owner":         e.owner,
				"lease_expires": domain.FormatTimestamp(lease.ExpiresAt),
			},
		}}, nil, false); err != nil {
		return session.result(err), err
	}
	e.logger.Info("run resumed", "run_id", run.ID.String(), "from", string(run.Status))

	return session.drive(ctx)
}

// nextPhaseName reports the phase the resumed run will work on next, for the
// run.resumed payload and for status reporting.
func (s *session) nextPhaseName() *string {
	states, err := s.engine.store.Phases(context.WithoutCancel(context.Background()), s.run.ID)
	if err != nil {
		return nil
	}
	for _, phase := range states {
		if !phase.Status.Terminal() {
			name := phase.Name
			return &name
		}
	}
	return nil
}
