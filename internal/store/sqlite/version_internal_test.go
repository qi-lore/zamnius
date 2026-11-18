package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationsMatchTheBuildSchemaVersion pins the invariant the migration list
// relies on: the newest shipped migration is the schema this build writes, and the
// history is contiguous from 1. Without it, shippedSchemaVersion could disagree with
// domain.SchemaVersion and a stale database would be silently refused (or worse,
// opened as current).
func TestMigrationsMatchTheBuildSchemaVersion(t *testing.T) {
	if got := shippedSchemaVersion(); got != domainSchemaVersion {
		t.Fatalf("shippedSchemaVersion() = %d, want domain.SchemaVersion = %d", got, domainSchemaVersion)
	}
	for i, step := range migrations {
		if step.version != i+1 {
			t.Errorf("migrations[%d].version = %d, want %d (contiguous from 1)", i, step.version, i+1)
		}
		if len(step.statements) == 0 {
			t.Errorf("migration %d carries no statements", step.version)
		}
	}
}

// TestMigrateAppliesAndRecordsAPendingMigration proves the migration machinery
// runs each pending step once and records it in schema_migrations — the "runs a
// recorded migration" half of FR-017. The proof needs a migration this build does
// not ship, so one is appended for the test and removed afterwards; the shipped list
// is append-only in production.
func TestMigrateAppliesAndRecordsAPendingMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	saved := migrations
	t.Cleanup(func() { migrations = saved })
	next := domainSchemaVersion + 1
	migrations = append(append([]migration(nil), saved...), migration{
		version: next,
		statements: []string{
			`CREATE TABLE migration_probe (id INTEGER PRIMARY KEY);`,
		},
	})

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	version, err := first.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != next {
		t.Fatalf("SchemaVersion() = %d, want %d", version, next)
	}
	// The statements really ran: the probe table exists.
	if _, err := first.DB().ExecContext(ctx, `INSERT INTO migration_probe (id) VALUES (1)`); err != nil {
		t.Fatalf("migration statement did not take effect: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopening must not replay the migration: the recorded version is the guard.
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = second.Close() }()

	var applied int
	if err := second.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("recorded migrations = %d, want %d (each step once)", applied, len(migrations))
	}
	var kept int
	if err := second.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migration_probe`).Scan(&kept); err != nil {
		t.Fatalf("reopen lost the migrated table: %v", err)
	}
	if kept != 1 {
		t.Errorf("rows surviving the reopen = %d, want 1", kept)
	}
}
