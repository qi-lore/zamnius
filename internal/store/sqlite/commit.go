package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// eventEnvelope is the exact byte shape an event hash is computed over. Adding a
// field here changes every future hash, so it is a format decision, not a detail.
type eventEnvelope struct {
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Phase     string          `json:"phase"`
	AttemptID string          `json:"attempt_id"`
	TS        string          `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// EventHash recomputes an event's chained digest from its durable columns. The
// audit package uses it for verification, which is why it is exported: two
// implementations of the chain rule would eventually disagree.
func EventHash(prevHash, runID, eventType, actor, phase, attemptID, ts string, payload []byte) (string, error) {
	envelope := eventEnvelope{
		RunID:     runID,
		Type:      eventType,
		Actor:     actor,
		Phase:     phase,
		AttemptID: attemptID,
		TS:        ts,
		Payload:   json.RawMessage(payload),
	}
	canonical, err := domain.CanonicalJSON(envelope)
	if err != nil {
		return "", fmt.Errorf("canonicalise event: %w", err)
	}
	return domain.ChainHash(prevHash, canonical), nil
}

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
		hash, err := EventHash(prevHash, runID.String(), string(draft.Type), draft.Actor, draft.Phase, attemptID, ts, payload)
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

func applyRunDelta(ctx context.Context, tx *sql.Tx, runID uuid.UUID, delta domain.RunDelta, now time.Time) error {
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
	args = append(args, runID.String())

	query := "UPDATE runs SET " + joinComma(sets) + " WHERE id = ?"
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("apply run delta: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("apply run delta: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("apply run delta for %s: %w", runID, domain.ErrNotFound)
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
	return nil
}

// CommitPhaseResult persists a phase transition and any run-level position change.
func (s *Store) CommitPhaseResult(ctx context.Context, r domain.PhaseCommit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit phase: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if err := upsertPhase(ctx, tx, r.Phase); err != nil {
		return fmt.Errorf("commit phase: %w", err)
	}
	if r.Run != nil {
		if err := applyRunDelta(ctx, tx, r.Phase.RunID, *r.Run, now); err != nil {
			return fmt.Errorf("commit phase: %w", err)
		}
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
	return nil
}

// CommitRunResult persists a run-level transition with its events and decisions.
func (s *Store) CommitRunResult(ctx context.Context, r domain.RunCommit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if err := applyRunDelta(ctx, tx, r.RunID, r.Delta, now); err != nil {
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
	return nil
}
