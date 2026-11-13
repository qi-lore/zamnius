// Package sqlite is the reference StateStore: one transactional file holding run
// state, leases, the event log, and decision records.
//
// Three properties are load-bearing and must not be relaxed:
//
//  1. Every state change and its event commit in one transaction (FR-011).
//  2. events is append-only; the schema rejects UPDATE and DELETE, and no code
//     path offers them (data-model.md §6).
//  3. A lease is taken with one conditional UPDATE, never read-then-write, so two
//     processes cannot both believe they own a run (ADR-0004).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/prompt-fu/zamnius/internal/domain"

	// Pure-Go driver: no cgo, so the reference build cross-compiles and the
	// offline test suite works without a C toolchain (research.md R2).
	_ "modernc.org/sqlite"
)

// timelayout is fixed width on purpose. Lease expiry is compared as text, so a
// variable-width RFC3339 encoding would order incorrectly between values with
// and without fractional seconds ("...00Z" sorts after "...00.5Z").
const timeLayout = domain.TimestampLayout

func formatTime(t time.Time) string { return domain.FormatTimestamp(t) }

func parseTime(s string) (time.Time, error) {
	parsed, err := time.Parse(timeLayout, s)
	if err != nil {
		// Tolerate hand-edited rows and older writes that used RFC3339Nano.
		fallback, fallbackErr := time.Parse(time.RFC3339Nano, s)
		if fallbackErr != nil {
			return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
		}
		return fallback.UTC(), nil
	}
	return parsed, nil
}

// Store is a SQLite-backed StateStore.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates or opens the database at path, applies pending migrations, and
// returns a store holding a single writer connection.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("open sqlite store: path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("open sqlite store: create %s: %w", dir, err)
		}
	}

	// _txlock=immediate makes every BeginTx issue BEGIN IMMEDIATE, taking the write
	// lock up front. A deferred transaction would read, then upgrade to a write on a
	// stale WAL snapshot, which SQLite refuses with SQLITE_BUSY *without* consulting
	// busy_timeout. Across two processes that turns lease contention into a raw
	// "database is locked" instead of the contracted ErrRunAlreadyOwned, defeating
	// the single-writer guarantee the lease exists to provide (ADR-0004, ADR-0009).
	dsn := "file:" + url.PathEscape(path) +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	// One writer connection: SQLite serialises writes anyway, and a single
	// connection makes "state and event commit together" trivially true.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db, path: path}
	if err := store.verifyPragmas(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// verifyPragmas fails loudly rather than running with weaker durability: a run
// that cannot record state must halt, never continue (FR-056).
func (s *Store) verifyPragmas(ctx context.Context) error {
	checks := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "2"}, // FULL
		{"foreign_keys", "1"},
	}
	for _, check := range checks {
		var got string
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+check.pragma).Scan(&got); err != nil {
			return fmt.Errorf("verify pragma %s: %w", check.pragma, err)
		}
		if got != check.want {
			return fmt.Errorf("verify pragma %s: got %q, want %q", check.pragma, got, check.want)
		}
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path reports the database file path, for diagnostics and tests.
func (s *Store) Path() string { return s.path }

// DB exposes the handle to in-package helpers and tests. It is deliberately not
// exported beyond internal/.
func (s *Store) DB() *sql.DB { return s.db }

const schemaVersionTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);`

const schemaV1 = `
CREATE TABLE runs (
  id                     TEXT PRIMARY KEY,
  definition_name        TEXT NOT NULL,
  definition_fingerprint TEXT NOT NULL,
  definition_json        TEXT NOT NULL,
  status                 TEXT NOT NULL,
  owner_id               TEXT,
  lease_expires_at       TEXT,
  current_phase          TEXT,
  outcome_reason         TEXT,
  budget_json            TEXT NOT NULL,
  spent_json             TEXT NOT NULL,
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL,
  record_version         INTEGER NOT NULL DEFAULT 1,
  schema_version         INTEGER NOT NULL,
  CHECK (status IN ('pending','running','paused','succeeded','failed','cancelled')),
  CHECK ((owner_id IS NULL) = (lease_expires_at IS NULL))
);

CREATE TABLE phases (
  run_id         TEXT NOT NULL REFERENCES runs(id),
  name           TEXT NOT NULL,
  ordinal        INTEGER NOT NULL,
  status         TEXT NOT NULL,
  started_at     TEXT,
  ended_at       TEXT,
  attempt_count  INTEGER NOT NULL DEFAULT 0,
  join_result    TEXT,
  failure_reason TEXT,
  degraded       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, name),
  CHECK (status IN ('pending','running','succeeded','failed','degraded','cancelled','skipped'))
);

CREATE TABLE attempts (
  id                TEXT PRIMARY KEY,
  run_id            TEXT NOT NULL REFERENCES runs(id),
  phase             TEXT NOT NULL,
  attempt_no        INTEGER NOT NULL,
  status            TEXT NOT NULL,
  agent_name        TEXT NOT NULL,
  model_provider    TEXT NOT NULL,
  model_name        TEXT NOT NULL,
  input_fingerprint TEXT NOT NULL,
  prompt_version    TEXT NOT NULL,
  started_at        TEXT NOT NULL,
  ended_at          TEXT,
  error_class       TEXT,
  error_detail      TEXT,
  usage_json        TEXT NOT NULL,
  restarted_from    TEXT,
  UNIQUE (run_id, phase, attempt_no),
  CHECK (status IN ('running','succeeded','failed','abandoned'))
);

CREATE TABLE artifacts (
  id            TEXT PRIMARY KEY,
  sha256        TEXT NOT NULL UNIQUE,
  run_id        TEXT NOT NULL REFERENCES runs(id),
  phase         TEXT NOT NULL,
  attempt_id    TEXT REFERENCES attempts(id),
  kind          TEXT NOT NULL,
  media_type    TEXT NOT NULL,
  size_bytes    INTEGER NOT NULL,
  relative_path TEXT NOT NULL,
  created_at    TEXT NOT NULL
);

CREATE TABLE events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     TEXT NOT NULL,
  ts         TEXT NOT NULL,
  type       TEXT NOT NULL,
  actor      TEXT NOT NULL,
  phase      TEXT,
  attempt_id TEXT,
  payload    TEXT NOT NULL,
  prev_hash  TEXT,
  hash       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS events_run_seq ON events(run_id, seq);

CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;

CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;

CREATE TABLE decisions (
  id         TEXT PRIMARY KEY,
  run_id     TEXT NOT NULL,
  phase      TEXT,
  attempt_id TEXT,
  ts         TEXT NOT NULL,
  kind       TEXT NOT NULL,
  summary    TEXT NOT NULL,
  rationale  TEXT NOT NULL,
  evidence   TEXT NOT NULL
);`
