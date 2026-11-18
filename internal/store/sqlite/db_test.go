package sqlite_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
)

func TestOpenAppliesSchemaAndPragmas(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != domain.SchemaVersion {
		t.Fatalf("SchemaVersion() = %d, want %d", version, domain.SchemaVersion)
	}

	// Durability is a behavioural requirement, not a footnote: a run that cannot
	// record must halt (FR-056), so the pragmas are asserted.
	var journalMode string
	if err := store.DB().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}
	var synchronous int
	if err := store.DB().QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if synchronous != 2 {
		t.Errorf("synchronous = %d, want 2 (FULL)", synchronous)
	}
	var foreignKeys int
	if err := store.DB().QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Error("foreign_keys must be ON")
	}
}

func TestEventsRejectUpdateAndDelete(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "one-phase")

	// Conformance test 4: the append-only guarantee must hold at the schema level,
	// not merely by coding convention.
	if _, err := store.DB().ExecContext(ctx, "UPDATE events SET actor = 'mallory' WHERE run_id = ?", run.ID.String()); err == nil {
		t.Fatal("UPDATE on events succeeded; it must be rejected")
	}
	if _, err := store.DB().ExecContext(ctx, "DELETE FROM events WHERE run_id = ?", run.ID.String()); err == nil {
		t.Fatal("DELETE on events succeeded; it must be rejected")
	}

	events, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("events disappeared after rejected mutations")
	}
	if events[0].Actor != domain.ActorSystem {
		t.Errorf("event actor = %q, want %q", events[0].Actor, domain.ActorSystem)
	}
}

// TestOpenRequiresAPathAndReportsIt pins two small parts of the constructor's
// contract: an empty path is refused rather than silently opening something, and
// Path reports exactly the file that was opened. It also proves Open creates the
// containing directory, so a fresh state directory needs no prior mkdir.
func TestOpenRequiresAPathAndReportsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, err := sqlite.Open(ctx, ""); err == nil {
		t.Fatal(`Open("") succeeded; an empty path must be refused`)
	} else if !strings.Contains(err.Error(), "path") {
		t.Errorf(`Open("") error = %v, want it to name the missing path`, err)
	}

	// A nested directory that does not exist yet: Open must create it.
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if got := store.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

// newStore opens a store in a per-test temporary directory and closes it on cleanup.
func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "zamnius.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

// createRun creates a run with the given pending phases and a synthetic definition.
func createRun(t *testing.T, store *sqlite.Store, phases ...string) domain.Run {
	t.Helper()

	states := make([]domain.PhaseState, 0, len(phases))
	for i, name := range phases {
		states = append(states, domain.PhaseState{
			Name:    name,
			Ordinal: i,
			Status:  domain.PhasePending,
		})
	}
	run, err := store.CreateRun(context.Background(), domain.RunSpec{
		ID:                    uuid.New(),
		DefinitionName:        "test-workflow",
		DefinitionFingerprint: domain.HashBytes([]byte("definition")),
		DefinitionJSON:        []byte(`{"version":"1","name":"test-workflow"}`),
		Phases:                states,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return run
}

func nowUTC() time.Time { return time.Now().UTC() }
