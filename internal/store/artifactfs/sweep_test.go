package artifactfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/store/artifactfs"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
)

func TestSweepRefusesWhileARunIsRunning(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	catalog := newCatalog(t)
	ctx := context.Background()

	run := createRun(t, catalog)
	if err := catalog.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Delta: domain.RunDelta{Status: domain.RunRunning},
	}); err != nil {
		t.Fatalf("mark run running: %v", err)
	}

	orphan, _ := writeOrphan(t, store, "orphan during a live run\n")

	result, err := store.Sweep(ctx, catalog)
	if !errors.Is(err, artifactfs.ErrWorkInFlight) {
		t.Fatalf("Sweep() error = %v, want ErrWorkInFlight", err)
	}
	if result.Reclaimed != 0 || result.Bytes != 0 {
		t.Errorf("refused sweep reclaimed %d files / %d bytes, want 0 / 0", result.Reclaimed, result.Bytes)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphan was deleted while a run was running: %v", err)
	}
}

func TestSweepReclaimsOrphansAndKeepsReferencedArtifacts(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	catalog := newCatalog(t)
	ctx := context.Background()

	// A pending run keeps the guard open (no run is running) but nothing about
	// it may be reclaimed either.
	run := createRun(t, catalog)

	referenced, err := store.Put(ctx, run.ID, "plan", "text/markdown", strings.NewReader("referenced content\n"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := catalog.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt:   attemptFor(run.ID, "plan", 1),
		Artifacts: []domain.ArtifactRef{referenced},
	}); err != nil {
		t.Fatalf("commit referenced artifact: %v", err)
	}

	orphanA, sizeA := writeOrphan(t, store, "first orphan\n")
	orphanB, sizeB := writeOrphan(t, store, "second orphan\n")

	result, err := store.Sweep(ctx, catalog)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.Reclaimed != 2 {
		t.Errorf("Reclaimed = %d, want 2", result.Reclaimed)
	}
	if result.Bytes != sizeA+sizeB {
		t.Errorf("Bytes = %d, want %d", result.Bytes, sizeA+sizeB)
	}
	if result.Scanned < 3 {
		t.Errorf("Scanned = %d, want at least 3 (two orphans plus one referenced file)", result.Scanned)
	}
	for _, path := range []string{orphanA, orphanB} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan %s survived the sweep: %v", path, err)
		}
	}
	// The referenced file must be intact: the sweep deletes files, never rows, so
	// a row left pointing at missing content would break the resume invariant.
	if err := store.Verify(ctx, referenced.SHA256); err != nil {
		t.Errorf("referenced artifact was removed: %v", err)
	}
}

func TestSweepLeavesMalformedAndForeignFiles(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	catalog := newCatalog(t)
	ctx := context.Background()
	createRun(t, catalog)

	// A non-digest file inside a well-formed shard directory: not content the
	// store owns, so the sweep must not touch it.
	shard := filepath.Join(store.Root(), "ab")
	if err := os.MkdirAll(shard, 0o750); err != nil {
		t.Fatalf("create shard dir: %v", err)
	}
	foreign := filepath.Join(shard, "not-a-digest")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	// A well-formed digest sitting in the wrong shard directory is not a path the
	// store ever writes, so it is left alone rather than reclaimed.
	digest := domain.HashBytes([]byte("misplaced\n"))
	wrongShard := "ff"
	if digest[:2] == wrongShard {
		wrongShard = "ee"
	}
	wrongDir := filepath.Join(store.Root(), wrongShard)
	if err := os.MkdirAll(wrongDir, 0o750); err != nil {
		t.Fatalf("create wrong shard dir: %v", err)
	}
	misplaced := filepath.Join(wrongDir, digest)
	if err := os.WriteFile(misplaced, []byte("misplaced\n"), 0o600); err != nil {
		t.Fatalf("write misplaced file: %v", err)
	}

	// One genuine orphan proves the filter still selects real garbage.
	orphan, _ := writeOrphan(t, store, "genuine orphan\n")

	result, err := store.Sweep(ctx, catalog)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.Reclaimed != 1 {
		t.Errorf("Reclaimed = %d, want 1 (only the genuine orphan)", result.Reclaimed)
	}
	for _, path := range []string{foreign, misplaced} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("non-candidate file %s was touched: %v", path, err)
		}
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("genuine orphan survived the sweep: %v", err)
	}
}

func TestSweepPropagatesCatalogErrors(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	catalogErr := errors.New("catalog unavailable")
	orphan, _ := writeOrphan(t, store, "orphan\n")

	cases := map[string]artifactfs.Catalog{
		"running count fails": fakeCatalog{runningErr: catalogErr},
		"artifact rows fail":  fakeCatalog{digestsErr: catalogErr},
	}
	for name, catalog := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := store.Sweep(context.Background(), catalog)
			if !errors.Is(err, catalogErr) {
				t.Fatalf("Sweep() error = %v, want %v", err, catalogErr)
			}
			if result.Reclaimed != 0 {
				t.Errorf("Reclaimed = %d after a catalog error, want 0", result.Reclaimed)
			}
			if _, err := os.Stat(orphan); err != nil {
				t.Errorf("orphan deleted despite a catalog error: %v", err)
			}
		})
	}
}

func TestSweepReclaimsWithCatalogPort(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	orphan, size := writeOrphan(t, store, "port orphan\n")

	result, err := store.Sweep(ctx, fakeCatalog{})
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.Reclaimed != 1 || result.Bytes != size {
		t.Errorf("Reclaimed/Bytes = %d/%d, want 1/%d", result.Reclaimed, result.Bytes, size)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan survived a sweep with an empty catalog: %v", err)
	}
}

// fakeCatalog is a minimal Catalog for the paths the real store cannot produce
// (an unavailable catalog) and for isolating the sweep from SQLite.
type fakeCatalog struct {
	digests    map[string]struct{}
	running    int
	runningErr error
	digestsErr error
}

func (f fakeCatalog) RunningRuns(context.Context) (int, error) { return f.running, f.runningErr }

func (f fakeCatalog) AllArtifactSHA256(context.Context) (map[string]struct{}, error) {
	return f.digests, f.digestsErr
}

// writeOrphan places content at its content-addressed path without committing a
// row, which is exactly the crash window Put can leave behind (contracts/store.md §4).
func writeOrphan(t *testing.T, store *artifactfs.Store, content string) (string, int64) {
	t.Helper()

	digest := domain.HashBytes([]byte(content))
	path := store.Path(digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create shard dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	return path, int64(len(content))
}

func newCatalog(t *testing.T) *sqlite.Store {
	t.Helper()

	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "zamnius.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createRun(t *testing.T, store *sqlite.Store) domain.Run {
	t.Helper()

	run, err := store.CreateRun(context.Background(), domain.RunSpec{
		ID:                    uuid.New(),
		DefinitionName:        "sweep-workflow",
		DefinitionFingerprint: domain.HashBytes([]byte("definition")),
		DefinitionJSON:        []byte(`{"version":"1","name":"sweep-workflow"}`),
		Phases:                []domain.PhaseState{{Name: "plan", Ordinal: 0, Status: domain.PhasePending}},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return run
}

func attemptFor(runID uuid.UUID, phase string, number int) domain.Attempt {
	return domain.Attempt{
		ID:               uuid.New(),
		RunID:            runID,
		Phase:            phase,
		AttemptNo:        number,
		Status:           domain.AttemptSucceeded,
		AgentName:        "planner",
		ModelProvider:    "fake",
		ModelName:        "fake-1",
		InputFingerprint: "fp",
		PromptVersion:    "v1",
		StartedAt:        time.Now().UTC(),
	}
}
