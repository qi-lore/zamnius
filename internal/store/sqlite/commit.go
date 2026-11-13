package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/fault"
)

// appendEvents writes events in order, chaining each to the run's previous hash.
// It must run inside the transaction that records the state change the events
// describe, otherwise the audit trail and the state could disagree (FR-011).
func appendEvents(ctx context.Context, tx *sql.Tx, runID uuid.UUID, now time.Time, drafts []domain.EventDraft) ([]domain.Event, error) {
	if len(drafts) == 0 {
		return nil, nil
	}

	prevHash, err := lastEventHash(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	ts := formatTime(now)

	written := make([]domain.Event, 0, len(drafts))
	for _, draft := range drafts {
		if !draft.Type.Valid() {
			return nil, fmt.Errorf("append event: %q is outside the event vocabulary", draft.Type)
		}
		payload, err := domain.CanonicalJSON(draft.Payload)
		if err != nil {
			return nil, fmt.Errorf("append event %s: canonicalise payload: %w", draft.Type, err)
		}
		attemptID := ""
		if draft.AttemptID != uuid.Nil {
			attemptID = draft.AttemptID.String()
		}
		hash, err := domain.EventHash(prevHash, runID.String(), string(draft.Type), draft.Actor, draft.Phase, attemptID, ts, payload)
		if err != nil {
			return nil, err
		}

		var phaseValue any
		if draft.Phase != "" {
			phaseValue = draft.Phase
		}
		var attemptValue any
		if attemptID != "" {
			attemptValue = attemptID
		}
		var prevValue any
		if prevHash != "" {
			prevValue = prevHash
		}

		result, err := tx.ExecContext(ctx,
			`INSERT INTO events (run_id, ts, type, actor, phase, attempt_id, payload, prev_hash, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID.String(), ts, string(draft.Type), draft.Actor, phaseValue, attemptValue, string(payload), prevValue, hash)
		if err != nil {
			return nil, fmt.Errorf("append event %s: %w", draft.Type, err)
		}
		seq, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("append event %s: read seq: %w", draft.Type, err)
		}

		written = append(written, domain.Event{
			Seq:       seq,
			RunID:     runID,
			TS:        now.UTC(),
			Type:      string(draft.Type),
			Actor:     draft.Actor,
			Phase:     optionalString(draft.Phase),
			AttemptID: optionalUUID(attemptID),
			Payload:   payload,
			PrevHash:  prevHash,
			Hash:      hash,
		})
		prevHash = hash
	}
	return written, nil
}

func lastEventHash(ctx context.Context, tx *sql.Tx, runID uuid.UUID) (string, error) {
	var hash sql.NullString
	err := tx.QueryRowContext(ctx,
		"SELECT hash FROM events WHERE run_id = ? ORDER BY seq DESC LIMIT 1", runID.String()).Scan(&hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read last event hash: %w", err)
	default:
		return hash.String, nil
	}
}

func insertDecisions(ctx context.Context, tx *sql.Tx, runID uuid.UUID, now time.Time, drafts []domain.DecisionDraft) error {
	for _, draft := range drafts {
		if !draft.Kind.Valid() {
			return fmt.Errorf("insert decision: %q is outside the decision kinds", draft.Kind)
		}
		evidence, err := domain.CanonicalJSON(draft.Evidence)
		if err != nil {
			return fmt.Errorf("insert decision %s: canonicalise evidence: %w", draft.Kind, err)
		}
		var attemptValue any
		if draft.AttemptID != uuid.Nil {
			attemptValue = draft.AttemptID.String()
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO decisions (id, run_id, phase, attempt_id, ts, kind, summary, rationale, evidence)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), runID.String(), optionalString(draft.Phase), attemptValue,
			formatTime(now), string(draft.Kind), draft.Summary, draft.Rationale, string(evidence))
		if err != nil {
			return fmt.Errorf("insert decision %s: %w", draft.Kind, err)
		}
	}
	return nil
}

func insertArtifacts(ctx context.Context, tx *sql.Tx, runID uuid.UUID, refs []domain.ArtifactRef) error {
	for _, ref := range refs {
		// Content is write-once and addressed by hash: identical bytes from two
		// attempts are one artifact (data-model.md §5 invariant 1), so a second
		// insert of the same digest is a no-op rather than a conflict.
		var attemptValue any
		if ref.AttemptID != nil {
			attemptValue = ref.AttemptID.String()
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO artifacts (id, sha256, run_id, phase, attempt_id, kind, media_type, size_bytes, relative_path, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(sha256) DO NOTHING`,
			ref.ID.String(), ref.SHA256, runID.String(), ref.Phase, attemptValue,
			ref.Kind, ref.MediaType, ref.SizeBytes, ref.RelativePath, formatTime(ref.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert artifact %s: %w", ref.SHA256, err)
		}
	}
	return nil
}

func upsertPhase(ctx context.Context, tx *sql.Tx, phase domain.PhaseState) error {
	var joinJSON any
	if phase.JoinResult != nil {
		encoded, err := domain.CanonicalJSON(phase.JoinResult)
		if err != nil {
			return fmt.Errorf("encode join result: %w", err)
		}
		joinJSON = string(encoded)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO phases (run_id, name, ordinal, status, started_at, ended_at, attempt_count, join_result, failure_reason, degraded)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, name) DO UPDATE SET
		   ordinal = excluded.ordinal,
		   status = excluded.status,
		   started_at = excluded.started_at,
		   ended_at = excluded.ended_at,
		   attempt_count = excluded.attempt_count,
		   join_result = excluded.join_result,
		   failure_reason = excluded.failure_reason,
		   degraded = excluded.degraded`,
		phase.RunID.String(), phase.Name, phase.Ordinal, string(phase.Status),
		nullableTime(phase.StartedAt), nullableTime(phase.EndedAt), phase.AttemptCount,
		joinJSON, nullableStringValue(phase.FailureReason), phase.Degraded)
	if err != nil {
		return fmt.Errorf("upsert phase %s: %w", phase.Name, err)
	}
	return nil
}

func upsertAttempt(ctx context.Context, tx *sql.Tx, attempt domain.Attempt) error {
	usage, err := domain.CanonicalJSON(attempt.Usage)
	if err != nil {
		return fmt.Errorf("encode attempt usage: %w", err)
	}
	var errorClass any
	if attempt.ErrorClass != "" {
		errorClass = string(attempt.ErrorClass)
	}
	var restarted any
	if attempt.RestartedFrom != nil {
		restarted = attempt.RestartedFrom.String()
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO attempts (id, run_id, phase, attempt_no, status, agent_name, model_provider, model_name,
		                       input_fingerprint, prompt_version, started_at, ended_at, error_class, error_detail,
		                       usage_json, restarted_from)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   status = excluded.status,
		   ended_at = excluded.ended_at,
		   error_class = excluded.error_class,
		   error_detail = excluded.error_detail,
		   usage_json = excluded.usage_json,
		   restarted_from = excluded.restarted_from`,
		attempt.ID.String(), attempt.RunID.String(), attempt.Phase, attempt.AttemptNo,
		string(attempt.Status), attempt.AgentName, attempt.ModelProvider, attempt.ModelName,
		attempt.InputFingerprint, attempt.PromptVersion, formatTime(attempt.StartedAt),
		nullableTime(attempt.EndedAt), errorClass, nullableStringValue(attempt.ErrorDetail),
		string(usage), restarted)
	if err != nil {
		return fmt.Errorf("upsert attempt %d of %s: %w", attempt.AttemptNo, attempt.Phase, err)
	}
	return nil
}

func applyRunDelta(ctx context.Context, tx *sql.Tx, runID uuid.UUID, expectedVersion int, delta domain.RunDelta, now time.Time) error {
	// Spent accumulates rather than overwrites: two branches committing usage
	// concurrently must not erase each other's tokens.
	current, err := loadConsumption(ctx, tx, runID)
	if err != nil {
		return err
	}
	spent := current.Add(delta.SpentDelta)
	spentJSON, err := domain.CanonicalJSON(spent)
	if err != nil {
		return fmt.Errorf("encode spend: %w", err)
	}

	sets := []string{"updated_at = ?", "spent_json = ?", "record_version = record_version + 1"}
	args := []any{formatTime(now), string(spentJSON)}
	if delta.Status != "" {
		if !delta.Status.Valid() {
			return fmt.Errorf("apply run delta: %q is not a valid run status", delta.Status)
		}
		sets = append(sets, "status = ?")
		args = append(args, string(delta.Status))
	}
	if delta.CurrentPhase != nil {
		sets = append(sets, "current_phase = ?")
		args = append(args, *delta.CurrentPhase)
	}
	if delta.OutcomeReason != nil {
		sets = append(sets, "outcome_reason = ?")
		args = append(args, *delta.OutcomeReason)
	}
	if delta.SchemaVersion > 0 {
		sets = append(sets, "schema_version = ?")
		args = append(args, delta.SchemaVersion)
	}
	args = append(args, runID.String())

	query := "UPDATE runs SET " + joinComma(sets) + " WHERE id = ?"
	if expectedVersion > 0 {
		// The version predicate is what makes the guard optimistic: a stale writer
		// matches no row and is told to re-read rather than silently overwriting.
		query += " AND record_version = ?"
		args = append(args, expectedVersion)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("apply run delta: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("apply run delta: %w", err)
	}
	if affected == 0 {
		return lostWrite(ctx, tx, runID, expectedVersion)
	}
	return nil
}

// lostWrite explains why an update matched no row. A missing run and a stale
// version are different operator problems, so they must not collapse into one
// error (FR-052).
func lostWrite(ctx context.Context, tx *sql.Tx, runID uuid.UUID, expectedVersion int) error {
	var actual int
	err := tx.QueryRowContext(ctx, "SELECT record_version FROM runs WHERE id = ?", runID.String()).Scan(&actual)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("commit run %s: %w", runID, domain.ErrNotFound)
	case err != nil:
		return fmt.Errorf("read run version: %w", err)
	default:
		return fmt.Errorf("commit run %s at record_version %d, stored %d: %w",
			runID, expectedVersion, actual, domain.ErrStaleRecord)
	}
}

// guardRunVersion applies the optimistic guard for a commit that changes only
// phase rows: the run row still advances so a stale owner is detected.
func guardRunVersion(ctx context.Context, tx *sql.Tx, runID uuid.UUID, expectedVersion int) error {
	if expectedVersion <= 0 {
		return nil
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE runs SET record_version = record_version + 1 WHERE id = ? AND record_version = ?",
		runID.String(), expectedVersion)
	if err != nil {
		return fmt.Errorf("guard run version: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("guard run version: %w", err)
	}
	if affected == 0 {
		return lostWrite(ctx, tx, runID, expectedVersion)
	}
	return nil
}

func loadConsumption(ctx context.Context, tx *sql.Tx, runID uuid.UUID) (domain.Consumption, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT spent_json FROM runs WHERE id = ?", runID.String()).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Consumption{}, fmt.Errorf("load run %s: %w", runID, domain.ErrNotFound)
		}
		return domain.Consumption{}, fmt.Errorf("load spend: %w", err)
	}
	var spent domain.Consumption
	if err := json.Unmarshal([]byte(raw), &spent); err != nil {
		return domain.Consumption{}, fmt.Errorf("decode spend: %w", err)
	}
	return spent, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalUUID(value string) *uuid.UUID {
	if value == "" {
		return nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func nullableStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

// CreateRun persists a run, its pending phase rows, and the run.created event in
// one transaction. The definition snapshot is stored with the run so resume never
// depends on the file the operator passed (data-model.md §2 invariant 4).
func (s *Store) CreateRun(ctx context.Context, spec domain.RunSpec) (domain.Run, error) {
	if spec.ID == uuid.Nil {
		return domain.Run{}, fmt.Errorf("create run: id is required")
	}
	if spec.DefinitionFingerprint == "" {
		return domain.Run{}, fmt.Errorf("create run: definition fingerprint is required")
	}
	if len(spec.DefinitionJSON) == 0 {
		return domain.Run{}, fmt.Errorf("create run: definition snapshot is required")
	}
	if len(spec.Phases) == 0 {
		return domain.Run{}, fmt.Errorf("create run: at least one phase is required")
	}

	now := spec.Now.UTC()
	if spec.Now.IsZero() {
		now = time.Now().UTC()
	}
	budgetJSON, err := domain.CanonicalJSON(spec.Budget)
	if err != nil {
		return domain.Run{}, fmt.Errorf("create run: encode budget: %w", err)
	}
	spentJSON, err := domain.CanonicalJSON(domain.Consumption{})
	if err != nil {
		return domain.Run{}, fmt.Errorf("create run: encode spend: %w", err)
	}

	fault.Hit(fault.BeforeCommit)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, fmt.Errorf("create run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO runs (id, definition_name, definition_fingerprint, definition_json, status,
		                   owner_id, lease_expires_at, current_phase, outcome_reason,
		                   budget_json, spent_json, created_at, updated_at, record_version, schema_version)
		 VALUES (?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, ?, ?, ?, ?, 1, ?)`,
		spec.ID.String(), spec.DefinitionName, spec.DefinitionFingerprint, string(spec.DefinitionJSON),
		string(domain.RunPending), string(budgetJSON), string(spentJSON),
		formatTime(now), formatTime(now), domain.SchemaVersion)
	if err != nil {
		return domain.Run{}, fmt.Errorf("create run: insert: %w", err)
	}

	phaseNames := make([]string, 0, len(spec.Phases))
	for _, phase := range spec.Phases {
		phase.RunID = spec.ID
		if err := upsertPhase(ctx, tx, phase); err != nil {
			return domain.Run{}, fmt.Errorf("create run: %w", err)
		}
		phaseNames = append(phaseNames, phase.Name)
	}

	if _, err := appendEvents(ctx, tx, spec.ID, now, []domain.EventDraft{{
		Type:  domain.EventRunCreated,
		Actor: domain.ActorSystem,
		Payload: map[string]any{
			"definition_name":        spec.DefinitionName,
			"definition_fingerprint": spec.DefinitionFingerprint,
			"schema_version":         domain.SchemaVersion,
			"phases":                 phaseNames,
		},
	}}); err != nil {
		return domain.Run{}, fmt.Errorf("create run: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.Run{}, fmt.Errorf("create run: commit: %w", err)
	}
	fault.Hit(fault.AfterCommit)

	return domain.Run{
		ID:                    spec.ID,
		DefinitionName:        spec.DefinitionName,
		DefinitionFingerprint: spec.DefinitionFingerprint,
		DefinitionJSON:        spec.DefinitionJSON,
		Status:                domain.RunPending,
		Budget:                spec.Budget,
		CreatedAt:             now,
		UpdatedAt:             now,
		RecordVersion:         1,
		SchemaVersion:         domain.SchemaVersion,
	}, nil
}

// CommitAttemptResult persists one attempt outcome atomically: attempt row,
// artifact rows, the phase's new counters, and the describing events/decisions.
func (s *Store) CommitAttemptResult(ctx context.Context, r domain.AttemptCommit) error {
	fault.Hit(fault.BeforeCommit)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit attempt: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if err := upsertAttempt(ctx, tx, r.Attempt); err != nil {
		return fmt.Errorf("commit attempt: %w", err)
	}
	if err := insertArtifacts(ctx, tx, r.Attempt.RunID, r.Artifacts); err != nil {
		return fmt.Errorf("commit attempt: %w", err)
	}
	if r.Phase != nil {
		if err := upsertPhase(ctx, tx, *r.Phase); err != nil {
			return fmt.Errorf("commit attempt: %w", err)
		}
	}
	if _, err := appendEvents(ctx, tx, r.Attempt.RunID, now, r.Events); err != nil {
		return fmt.Errorf("commit attempt: %w", err)
	}
	if err := insertDecisions(ctx, tx, r.Attempt.RunID, now, r.Decisions); err != nil {
		return fmt.Errorf("commit attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attempt: commit: %w", err)
	}
	fault.Hit(fault.AfterCommit)
	return nil
}

// CommitPhaseResult persists a phase transition and any run-level position change.
func (s *Store) CommitPhaseResult(ctx context.Context, r domain.PhaseCommit) error {
	fault.Hit(fault.BeforeCommit)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit phase: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if err := upsertPhase(ctx, tx, r.Phase); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if err := insertArtifacts(ctx, tx, r.Phase.RunID, r.Artifacts); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if r.Run != nil {
		if err := applyRunDelta(ctx, tx, r.Phase.RunID, r.ExpectedVersion, *r.Run, now); err != nil {
			return fmt.Errorf("commit phase: %w", err)
		}
	} else if err := guardRunVersion(ctx, tx, r.Phase.RunID, r.ExpectedVersion); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if _, err := appendEvents(ctx, tx, r.Phase.RunID, now, r.Events); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if err := insertDecisions(ctx, tx, r.Phase.RunID, now, r.Decisions); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit phase: commit: %w", err)
	}
	fault.Hit(fault.AfterCommit)
	return nil
}

// CommitRunResult persists a run-level transition with its events and decisions.
func (s *Store) CommitRunResult(ctx context.Context, r domain.RunCommit) error {
	fault.Hit(fault.BeforeCommit)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if err := applyRunDelta(ctx, tx, r.RunID, r.ExpectedVersion, r.Delta, now); err != nil {
		return fmt.Errorf("commit run: %w", err)
	}
	if _, err := appendEvents(ctx, tx, r.RunID, now, r.Events); err != nil {
		return fmt.Errorf("commit run: %w", err)
	}
	if err := insertDecisions(ctx, tx, r.RunID, now, r.Decisions); err != nil {
		return fmt.Errorf("commit run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run: commit: %w", err)
	}
	fault.Hit(fault.AfterCommit)
	return nil
}
