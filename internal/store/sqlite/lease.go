package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// AcquireLease takes ownership of a run only if it is unowned or its lease has
// expired. The decision is made by the conditional UPDATE itself: a read followed
// by a write would let two processes both observe "unowned" and both win
// (ADR-0004, FR-016).
func (s *Store) AcquireLease(ctx context.Context, id uuid.UUID, owner string, ttl time.Duration) (domain.Lease, error) {
	if owner == "" {
		return domain.Lease{}, fmt.Errorf("acquire lease: owner is required")
	}
	if ttl <= 0 {
		return domain.Lease{}, fmt.Errorf("acquire lease: ttl must be positive")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Lease{}, fmt.Errorf("acquire lease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	until := now.Add(ttl)

	// Read the prior owner only to describe a takeover; it never decides the
	// outcome. The UPDATE below does.
	var (
		previousOwner  sql.NullString
		previousExpiry sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		"SELECT owner_id, lease_expires_at FROM runs WHERE id = ?", id.String()).
		Scan(&previousOwner, &previousExpiry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Lease{}, fmt.Errorf("acquire lease for %s: %w", id, domain.ErrNotFound)
		}
		return domain.Lease{}, fmt.Errorf("acquire lease: %w", err)
	}

	var recordVersion int
	err = tx.QueryRowContext(ctx,
		`UPDATE runs
		    SET owner_id = ?, lease_expires_at = ?, record_version = record_version + 1
		  WHERE id = ?
		    AND (lease_expires_at IS NULL OR lease_expires_at < ?)
		 RETURNING record_version`,
		owner, formatTime(until), id.String(), formatTime(now)).Scan(&recordVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Lease{}, fmt.Errorf("acquire lease for %s as %s: %w",
				id, owner, domain.ErrRunAlreadyOwned)
		}
		return domain.Lease{}, fmt.Errorf("acquire lease: %w", err)
	}

	// A takeover of an expired lease is recorded with the evidence, so the audit
	// trail explains why two owners appear in one run's history (FR-016).
	if previousOwner.Valid && previousOwner.String != owner {
		payload := map[string]any{
			"previous_owner":   previousOwner.String,
			"previous_expiry":  previousExpiry.String,
			"new_owner":        owner,
			"lease_expires_at": formatTime(until),
		}
		if _, err := appendEvents(ctx, tx, id, now, []domain.EventDraft{{
			Type:    domain.EventRunTakenOver,
			Actor:   domain.ActorSystem,
			Payload: payload,
		}}); err != nil {
			return domain.Lease{}, fmt.Errorf("acquire lease: record takeover: %w", err)
		}
		if err := insertDecisions(ctx, tx, id, now, []domain.DecisionDraft{{
			Kind:      domain.DecisionTakeover,
			Summary:   "lease taken over from " + previousOwner.String,
			Rationale: "the previous lease expired before it was renewed, so ownership passed to " + owner,
			Evidence:  payload,
		}}); err != nil {
			return domain.Lease{}, fmt.Errorf("acquire lease: record takeover decision: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return domain.Lease{}, fmt.Errorf("acquire lease: commit: %w", err)
	}
	return domain.Lease{RunID: id, Owner: owner, ExpiresAt: until, RecordVersion: recordVersion}, nil
}

// RenewLease extends a lease the caller still holds. Zero rows means ownership was
// lost — a takeover — and the caller must stop writing rather than continue
// believing it owns the run.
func (s *Store) RenewLease(ctx context.Context, l domain.Lease, ttl time.Duration) (domain.Lease, error) {
	if ttl <= 0 {
		return domain.Lease{}, fmt.Errorf("renew lease: ttl must be positive")
	}
	now := time.Now().UTC()
	until := now.Add(ttl)

	var recordVersion int
	err := s.db.QueryRowContext(ctx,
		`UPDATE runs
		    SET lease_expires_at = ?, record_version = record_version + 1
		  WHERE id = ? AND owner_id = ?
		 RETURNING record_version`,
		formatTime(until), l.RunID.String(), l.Owner).Scan(&recordVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Lease{}, fmt.Errorf("renew lease for %s as %s: %w",
				l.RunID, l.Owner, domain.ErrRunAlreadyOwned)
		}
		return domain.Lease{}, fmt.Errorf("renew lease: %w", err)
	}
	return domain.Lease{RunID: l.RunID, Owner: l.Owner, ExpiresAt: until, RecordVersion: recordVersion}, nil
}

// ReleaseLease drops ownership so another process may resume immediately instead
// of waiting for expiry.
func (s *Store) ReleaseLease(ctx context.Context, l domain.Lease) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs
		    SET owner_id = NULL, lease_expires_at = NULL, record_version = record_version + 1
		  WHERE id = ? AND owner_id = ?`,
		l.RunID.String(), l.Owner)
	if err != nil {
		return fmt.Errorf("release lease for %s: %w", l.RunID, err)
	}
	return nil
}
