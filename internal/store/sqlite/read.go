package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/qi-lore/zamnius/internal/domain"
)

// LoadRun reads one run row.
func (s *Store) LoadRun(ctx context.Context, id uuid.UUID) (domain.Run, error) {
	var (
		run          domain.Run
		definitionJS string
		status       string
		owner        sql.NullString
		lease        sql.NullString
		currentPhase sql.NullString
		outcome      sql.NullString
		budgetJSON   string
		spentJSON    string
		createdAt    string
		updatedAt    string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, definition_name, definition_fingerprint, definition_json, status,
		        owner_id, lease_expires_at, current_phase, outcome_reason,
		        budget_json, spent_json, created_at, updated_at, record_version, schema_version
		   FROM runs WHERE id = ?`, id.String()).
		Scan(&run.ID, &run.DefinitionName, &run.DefinitionFingerprint, &definitionJS, &status,
			&owner, &lease, &currentPhase, &outcome,
			&budgetJSON, &spentJSON, &createdAt, &updatedAt, &run.RecordVersion, &run.SchemaVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, fmt.Errorf("load run %s: %w", id, domain.ErrNotFound)
		}
		return domain.Run{}, fmt.Errorf("load run %s: %w", id, err)
	}

	run.DefinitionJSON = []byte(definitionJS)
	run.Status = domain.RunStatus(status)
	if err := json.Unmarshal([]byte(budgetJSON), &run.Budget); err != nil {
		return domain.Run{}, fmt.Errorf("load run %s: decode budget: %w", id, err)
	}
	if err := json.Unmarshal([]byte(spentJSON), &run.Spent); err != nil {
		return domain.Run{}, fmt.Errorf("load run %s: decode spend: %w", id, err)
	}
	if run.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Run{}, err
	}
	if run.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Run{}, err
	}
	if owner.Valid {
		value := owner.String
		run.OwnerID = &value
	}
	if lease.Valid {
		expires, err := parseTime(lease.String)
		if err != nil {
			return domain.Run{}, err
		}
		run.LeaseExpiresAt = &expires
	}
	if currentPhase.Valid {
		value := currentPhase.String
		run.CurrentPhase = &value
	}
	if outcome.Valid {
		value := outcome.String
		run.OutcomeReason = &value
	}
	return run, nil
}

// Phases reads a run's phase rows in declared order.
func (s *Store) Phases(ctx context.Context, runID uuid.UUID) ([]domain.PhaseState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, name, ordinal, status, started_at, ended_at, attempt_count,
		        join_result, failure_reason, degraded
		   FROM phases WHERE run_id = ? ORDER BY ordinal`, runID.String())
	if err != nil {
		return nil, fmt.Errorf("read phases of %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var phases []domain.PhaseState
	for rows.Next() {
		var (
			phase     domain.PhaseState
			status    string
			startedAt sql.NullString
			endedAt   sql.NullString
			joinJSON  sql.NullString
			failure   sql.NullString
			degraded  int
		)
		if err := rows.Scan(&phase.RunID, &phase.Name, &phase.Ordinal, &status,
			&startedAt, &endedAt, &phase.AttemptCount, &joinJSON, &failure, &degraded); err != nil {
			return nil, fmt.Errorf("read phases of %s: %w", runID, err)
		}
		phase.Status = domain.PhaseStatus(status)
		phase.Degraded = degraded != 0
		if startedAt.Valid {
			value, err := parseTime(startedAt.String)
			if err != nil {
				return nil, err
			}
			phase.StartedAt = &value
		}
		if endedAt.Valid {
			value, err := parseTime(endedAt.String)
			if err != nil {
				return nil, err
			}
			phase.EndedAt = &value
		}
		if joinJSON.Valid {
			var join domain.JoinResult
			if err := json.Unmarshal([]byte(joinJSON.String), &join); err != nil {
				return nil, fmt.Errorf("read phases of %s: decode join result: %w", runID, err)
			}
			phase.JoinResult = &join
		}
		if failure.Valid {
			value := failure.String
			phase.FailureReason = &value
		}
		phases = append(phases, phase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read phases of %s: %w", runID, err)
	}
	return phases, nil
}

// Attempts reads a phase's attempts in attempt order, each with its artifacts.
func (s *Store) Attempts(ctx context.Context, runID uuid.UUID, phase string) ([]domain.Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, phase, attempt_no, status, agent_name, model_provider, model_name,
		        input_fingerprint, prompt_version, started_at, ended_at, error_class, error_detail,
		        usage_json, restarted_from
		   FROM attempts WHERE run_id = ? AND phase = ? ORDER BY attempt_no`,
		runID.String(), phase)
	if err != nil {
		return nil, fmt.Errorf("read attempts of %s/%s: %w", runID, phase, err)
	}
	defer func() { _ = rows.Close() }()

	var attempts []domain.Attempt
	for rows.Next() {
		var (
			attempt     domain.Attempt
			status      string
			startedAt   string
			endedAt     sql.NullString
			errorClass  sql.NullString
			errorDetail sql.NullString
			usageJSON   string
			restarted   sql.NullString
		)
		if err := rows.Scan(&attempt.ID, &attempt.RunID, &attempt.Phase, &attempt.AttemptNo, &status,
			&attempt.AgentName, &attempt.ModelProvider, &attempt.ModelName,
			&attempt.InputFingerprint, &attempt.PromptVersion, &startedAt, &endedAt,
			&errorClass, &errorDetail, &usageJSON, &restarted); err != nil {
			return nil, fmt.Errorf("read attempts of %s/%s: %w", runID, phase, err)
		}
		attempt.Status = domain.AttemptStatus(status)
		if attempt.StartedAt, err = parseTime(startedAt); err != nil {
			return nil, err
		}
		if endedAt.Valid {
			value, err := parseTime(endedAt.String)
			if err != nil {
				return nil, err
			}
			attempt.EndedAt = &value
		}
		if errorClass.Valid {
			attempt.ErrorClass = domain.ErrorClass(errorClass.String)
		}
		if errorDetail.Valid {
			value := errorDetail.String
			attempt.ErrorDetail = &value
		}
		if err := json.Unmarshal([]byte(usageJSON), &attempt.Usage); err != nil {
			return nil, fmt.Errorf("read attempts of %s/%s: decode usage: %w", runID, phase, err)
		}
		if restarted.Valid {
			parsed, err := uuid.Parse(restarted.String)
			if err != nil {
				return nil, fmt.Errorf("read attempts of %s/%s: parse restarted_from: %w", runID, phase, err)
			}
			attempt.RestartedFrom = &parsed
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attempts of %s/%s: %w", runID, phase, err)
	}

	for i := range attempts {
		refs, err := s.artifactsOfAttempt(ctx, attempts[i].ID)
		if err != nil {
			return nil, err
		}
		attempts[i].Artifacts = refs
	}
	return attempts, nil
}

func (s *Store) artifactsOfAttempt(ctx context.Context, attemptID uuid.UUID) ([]domain.ArtifactRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, sha256, run_id, phase, attempt_id, kind, media_type, size_bytes, relative_path, created_at
		   FROM artifacts WHERE attempt_id = ? ORDER BY sha256`, attemptID.String())
	if err != nil {
		return nil, fmt.Errorf("read artifacts of attempt %s: %w", attemptID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanArtifacts(rows)
}

// Artifacts reads every artifact row of a run in a stable order.
func (s *Store) Artifacts(ctx context.Context, runID uuid.UUID) ([]domain.ArtifactRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, sha256, run_id, phase, attempt_id, kind, media_type, size_bytes, relative_path, created_at
		   FROM artifacts WHERE run_id = ? ORDER BY created_at, sha256`, runID.String())
	if err != nil {
		return nil, fmt.Errorf("read artifacts of %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanArtifacts(rows)
}

// AllArtifactSHA256 lists every content address known to the store, for orphan sweeps.
func (s *Store) AllArtifactSHA256(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT sha256 FROM artifacts")
	if err != nil {
		return nil, fmt.Errorf("read artifact digests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	digests := make(map[string]struct{})
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("read artifact digests: %w", err)
		}
		digests[digest] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read artifact digests: %w", err)
	}
	return digests, nil
}

// RunningRuns reports how many runs are currently marked running, which the
// orphan sweep uses to refuse deletion while work is in flight.
func (s *Store) RunningRuns(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE status = ?", string(domain.RunRunning)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count running runs: %w", err)
	}
	return count, nil
}

func scanArtifacts(rows *sql.Rows) ([]domain.ArtifactRef, error) {
	var refs []domain.ArtifactRef
	for rows.Next() {
		var (
			ref       domain.ArtifactRef
			attemptID sql.NullString
			createdAt string
		)
		if err := rows.Scan(&ref.ID, &ref.SHA256, &ref.RunID, &ref.Phase, &attemptID,
			&ref.Kind, &ref.MediaType, &ref.SizeBytes, &ref.RelativePath, &createdAt); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		if attemptID.Valid {
			parsed, err := uuid.Parse(attemptID.String)
			if err != nil {
				return nil, fmt.Errorf("scan artifact %s: parse attempt id: %w", ref.SHA256, err)
			}
			ref.AttemptID = &parsed
		}
		parsed, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		ref.CreatedAt = parsed
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan artifacts: %w", err)
	}
	return refs, nil
}

// Events reads a run's events with seq greater than fromSeq, in seq order. Seq is
// the only ordering authority (data-model.md §6).
func (s *Store) Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, run_id, ts, type, actor, phase, attempt_id, payload, prev_hash, hash
		   FROM events WHERE run_id = ? AND seq > ? ORDER BY seq`, runID.String(), fromSeq)
	if err != nil {
		return nil, fmt.Errorf("read events of %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var events []domain.Event
	for rows.Next() {
		var (
			event     domain.Event
			ts        string
			phase     sql.NullString
			attemptID sql.NullString
			payload   string
			prevHash  sql.NullString
		)
		if err := rows.Scan(&event.Seq, &event.RunID, &ts, &event.Type, &event.Actor,
			&phase, &attemptID, &payload, &prevHash, &event.Hash); err != nil {
			return nil, fmt.Errorf("read events of %s: %w", runID, err)
		}
		if event.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		if phase.Valid {
			value := phase.String
			event.Phase = &value
		}
		if attemptID.Valid {
			parsed, err := uuid.Parse(attemptID.String)
			if err != nil {
				return nil, fmt.Errorf("read events of %s: parse attempt id: %w", runID, err)
			}
			event.AttemptID = &parsed
		}
		event.Payload = json.RawMessage(payload)
		event.PrevHash = prevHash.String
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events of %s: %w", runID, err)
	}
	return events, nil
}

// Decisions reads a run's decision records in insertion order, which matches the
// order they were committed in the same transactions as their events.
func (s *Store) Decisions(ctx context.Context, runID uuid.UUID) ([]domain.Decision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, phase, attempt_id, ts, kind, summary, rationale, evidence
		   FROM decisions WHERE run_id = ? ORDER BY rowid`, runID.String())
	if err != nil {
		return nil, fmt.Errorf("read decisions of %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var decisions []domain.Decision
	for rows.Next() {
		var (
			decision  domain.Decision
			phase     sql.NullString
			attemptID sql.NullString
			ts        string
			kind      string
			evidence  string
		)
		if err := rows.Scan(&decision.ID, &decision.RunID, &phase, &attemptID, &ts,
			&kind, &decision.Summary, &decision.Rationale, &evidence); err != nil {
			return nil, fmt.Errorf("read decisions of %s: %w", runID, err)
		}
		if decision.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		decision.Kind = domain.DecisionKind(kind)
		if phase.Valid {
			value := phase.String
			decision.Phase = &value
		}
		if attemptID.Valid {
			parsed, err := uuid.Parse(attemptID.String)
			if err != nil {
				return nil, fmt.Errorf("read decisions of %s: parse attempt id: %w", runID, err)
			}
			decision.AttemptID = &parsed
		}
		decision.Evidence = json.RawMessage(evidence)
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read decisions of %s: %w", runID, err)
	}
	return decisions, nil
}
