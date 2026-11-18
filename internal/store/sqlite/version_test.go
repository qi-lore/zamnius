package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
)

// TestCheckRunVersionReportsTheSchemaGapAndRefusesNewerState proves the store tells
// the caller which way a run's recorded schema differs — older state is migratable,
// newer state is not — and that only the refusal is an error, with a hint naming
// the versions so the operator knows to upgrade (FR-017, data-model.md §10).
func TestCheckRunVersionReportsTheSchemaGapAndRefusesNewerState(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	status, err := store.CheckRunVersion(ctx, run)
	if err != nil {
		t.Fatalf("CheckRunVersion() for current schema error = %v", err)
	}
	if status.RunVersion != domain.SchemaVersion || status.StoreVersion != domain.SchemaVersion {
		t.Errorf("status = %+v, want both versions %d", status, domain.SchemaVersion)
	}
	if status.Older() || status.Newer() {
		t.Errorf("status = %+v reports a gap for matching versions", status)
	}

	older := run
	older.SchemaVersion = domain.SchemaVersion - 1
	status, err = store.CheckRunVersion(ctx, older)
	if err != nil {
		t.Fatalf("CheckRunVersion() for older state error = %v, want nil (it is migratable)", err)
	}
	if !status.Older() {
		t.Errorf("status = %+v does not report older state", status)
	}

	newer := run
	newer.SchemaVersion = domain.SchemaVersion + 1
	status, err = store.CheckRunVersion(ctx, newer)
	if !errors.Is(err, domain.ErrMigrationRefused) {
		t.Fatalf("CheckRunVersion() for newer state error = %v, want ErrMigrationRefused", err)
	}
	if !status.Newer() {
		t.Errorf("status = %+v does not report newer state", status)
	}

	hint := domain.HintOf(err)
	if hint == "" {
		t.Fatal("a schema refusal carries no hint; the operator cannot act on it")
	}
	for _, version := range []string{strconv.Itoa(newer.SchemaVersion), strconv.Itoa(domain.SchemaVersion)} {
		if !strings.Contains(hint, version) {
			t.Errorf("hint %q does not name schema version %s", hint, version)
		}
	}
	if !strings.Contains(strings.ToLower(hint), "upgrade") {
		t.Errorf("hint %q does not tell the operator to upgrade", hint)
	}
}

// TestOpenRefusesAStoreWrittenByANewerSchema proves a downgrade is refused at open
// rather than interpreted: a database whose highest applied migration is newer than
// this build cannot be read, and the message says which build is needed (FR-017).
func TestOpenRefusesAStoreWrittenByANewerSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")

	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	future := domain.SchemaVersion + 1
	if _, err := store.DB().ExecContext(ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		future, domain.FormatTimestamp(time.Now()),
	); err != nil {
		t.Fatalf("record a future migration: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	_, err = sqlite.Open(ctx, path)
	if !errors.Is(err, domain.ErrMigrationRefused) {
		t.Fatalf("open of newer state error = %v, want ErrMigrationRefused", err)
	}
	hint := domain.HintOf(err)
	if hint == "" {
		t.Fatal("a refused open carries no hint; the operator cannot act on it")
	}
	if !strings.Contains(hint, strconv.Itoa(future)) {
		t.Errorf("hint %q does not name schema version %d", hint, future)
	}
}
