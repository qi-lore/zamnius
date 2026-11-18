package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/audit"
	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
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

	// Schema version first: it decides whether this build may interpret the record
	// at all. Older state was migrated when the store opened and is recorded on the
	// run; newer state is refused, never guessed at (FR-017, data-model.md §10).
	schema, err := e.checkRunSchema(ctx, run)
	if err != nil {
		e.recordMigrationRefusal(ctx, run, schema, err)
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	def, err := definition.FromSnapshot(run.DefinitionJSON)
	if err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	// Drift is decided before anything is written, so a refused resume leaves the
	// record untouched; the override is recorded only once this process holds the
	// lease and is entitled to append to the run's history.
	drift, err := compareDefinitions(run, def, opts.Definition)
	if err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}
	if drift.drifted() && !opts.AllowDefinitionDrift {
		refusal := refuseDefinitionDrift(run, drift)
		return RunResult{RunID: run.ID, Status: run.Status, Err: refusal}, refusal
	}

	lease, err := e.store.AcquireLease(ctx, run.ID, e.owner, e.leaseTTL)
	if err != nil {
		// Someone else owns the run right now: the caller needs the live owner and
		// expiry to decide whether to wait (T051).
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	if drift.drifted() {
		if err := e.recordDefinitionDriftOverride(ctx, run, drift); err != nil {
			// Owning a run this process is not about to advance would block the
			// operator it just refused, so ownership is given back.
			if releaseErr := e.store.ReleaseLease(ctx, lease); releaseErr != nil {
				e.logger.Warn("could not release lease after an unrecorded drift override",
					"run_id", run.ID.String(), "error", releaseErr.Error())
			}
			return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
		}
	}

	if schema.Older() {
		if err := e.recordRunMigration(ctx, run, schema); err != nil {
			if releaseErr := e.store.ReleaseLease(ctx, lease); releaseErr != nil {
				e.logger.Warn("could not release lease after an unrecorded migration",
					"run_id", run.ID.String(), "error", releaseErr.Error())
			}
			return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
		}
		// The row now conforms to the store's schema, so a later resume records no
		// second migration.
		run.SchemaVersion = schema.StoreVersion
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

	// Every artifact row must still have the bytes it names. A row whose file is
	// missing or altered is an integrity failure with evidence, never a cue to
	// re-run the phase that produced it: rebuilding the content would silently
	// replace the record the run is audited against (contracts/store.md §5 step 3).
	refs, err := e.Artifacts(ctx, run.ID)
	if err != nil {
		return session.result(err), err
	}
	if problems := audit.VerifyArtifacts(ctx, refs, e.artifacts); len(problems) > 0 {
		return session.failOnIntegrity(ctx, "artifact integrity verification failed", problems)
	}

	if err := session.reconcileAttempts(ctx, events); err != nil {
		return session.result(err), err
	}

	position := session.nextPhaseName(ctx)
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

// checkRunSchema reports how the run's recorded schema compares with the store's.
// Stores that do not report a version cannot be compared, so nothing is migrated or
// refused on their behalf.
func (e *Engine) checkRunSchema(ctx context.Context, run domain.Run) (domain.RunSchemaStatus, error) {
	checker, ok := e.store.(interface {
		CheckRunVersion(ctx context.Context, run domain.Run) (domain.RunSchemaStatus, error)
	})
	if !ok {
		return domain.RunSchemaStatus{RunVersion: run.SchemaVersion, StoreVersion: run.SchemaVersion}, nil
	}
	return checker.CheckRunVersion(ctx, run)
}

// recordRunMigration writes the migration fact on the run and brings its row up to
// the store's schema in the same transaction. The bump is what makes the event
// once-only: a later resume sees matching versions and records nothing (FR-011).
func (e *Engine) recordRunMigration(ctx context.Context, run domain.Run, status domain.RunSchemaStatus) error {
	err := e.store.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Delta: domain.RunDelta{SchemaVersion: status.StoreVersion},
		Events: []domain.EventDraft{{
			Type:  domain.EventStateMigrated,
			Actor: domain.ActorSystem,
			Payload: map[string]any{
				"from_schema_version": status.RunVersion,
				"to_schema_version":   status.StoreVersion,
			},
		}},
	})
	if err != nil {
		return err
	}
	e.logger.Info("run state migrated",
		"run_id", run.ID.String(), "from_schema", status.RunVersion, "to_schema", status.StoreVersion)
	return nil
}

// recordMigrationRefusal leaves the refusal in the run's history when the record can
// be claimed: appending to a run's history requires ownership, so a process that
// cannot take the lease leaves the record to its owner and refuses anyway. The
// refusal the caller receives never depends on this succeeding.
func (e *Engine) recordMigrationRefusal(ctx context.Context, run domain.Run, status domain.RunSchemaStatus, refusal error) {
	lease, err := e.store.AcquireLease(ctx, run.ID, e.owner, e.leaseTTL)
	if err != nil {
		e.logger.Info("schema refusal not recorded: the run is owned",
			"run_id", run.ID.String(), "error", err.Error())
		return
	}
	defer func() {
		if releaseErr := e.store.ReleaseLease(ctx, lease); releaseErr != nil {
			e.logger.Warn("could not release lease after a schema refusal",
				"run_id", run.ID.String(), "error", releaseErr.Error())
		}
	}()

	if err := e.store.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Events: []domain.EventDraft{{
			Type:  domain.EventStateMigrationRefused,
			Actor: domain.ActorSystem,
			Payload: map[string]any{
				"run_schema_version":   status.RunVersion,
				"store_schema_version": status.StoreVersion,
				"message":              refusal.Error(),
			},
		}},
	}); err != nil {
		e.logger.Warn("could not record a schema refusal",
			"run_id", run.ID.String(), "error", err.Error())
	}
}

// nextPhaseName reports the phase a run will work on next: the first phase that
// has not reached a terminal state, in declared order. It is the resume position
// for both a paused run (the `current_phase` a pause records, T053) and a resumed
// one (the `current_phase` a resume records), so one definition of "where work
// stopped" serves both paths.
func (s *session) nextPhaseName(ctx context.Context) *string {
	states, err := s.engine.store.Phases(ctx, s.run.ID)
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
