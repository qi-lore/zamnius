package recoverability_test

// This file covers T050: a run recorded under an older schema is migrated and the
// migration is recorded on the run; a run recorded under a newer schema is refused
// with an actionable diagnostic and the refusal is recorded. Neither path guesses
// (FR-017, data-model.md §10).

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// TestResumeRecordsMigrationOfOlderState proves resuming state written by an older
// schema migrates it with a recorded fact: exactly one state.migrated event names
// both versions, the run row is brought up to the store's schema in the same commit,
// and the bump is what stops a second resume from recording the same migration
// again (FR-017).
func TestResumeRecordsMigrationOfOlderState(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-migrate", &answerer{}, fakes.NewIDSource())
	def := twoPhaseDefinition(t)
	runID := createRunForDefinition(t, f, def, mustFingerprint(t, def))

	older := domain.SchemaVersion - 1
	forgeSchemaVersion(t, f, runID, older)

	result, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() of older state error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status after migrating older state = %q, want succeeded", result.Status)
	}

	events := eventsOf(t, f, runID)
	migrated := eventsOfType(events, string(domain.EventStateMigrated))
	if len(migrated) != 1 {
		t.Fatalf("state.migrated events = %d, want exactly 1", len(migrated))
	}
	payload := payloadOf(t, migrated[0])
	if got := payload["from_schema_version"]; got != float64(older) {
		t.Errorf("migrated from_schema_version = %v, want %d", got, older)
	}
	if got := payload["to_schema_version"]; got != float64(domain.SchemaVersion) {
		t.Errorf("migrated to_schema_version = %v, want %d", got, domain.SchemaVersion)
	}

	run, err := f.store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.SchemaVersion != domain.SchemaVersion {
		t.Errorf("run schema_version after migration = %d, want %d", run.SchemaVersion, domain.SchemaVersion)
	}
	if second := eventsOfType(events, string(domain.EventStateMigrated)); len(second) != 1 {
		t.Errorf("a single resume recorded %d migrations, want 1", len(second))
	}
}

// TestResumeRefusesNewerStateAndRecordsRefusal proves a run written by a newer
// schema is refused rather than interpreted: the caller gets ErrMigrationRefused
// with a hint naming the schema it would need, the refusal is recorded on the run,
// the run is left untouched, and the lease taken to record it is given back
// (FR-017, data-model.md §10).
func TestResumeRefusesNewerStateAndRecordsRefusal(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-newer", &answerer{}, fakes.NewIDSource())
	def := twoPhaseDefinition(t)
	runID := createRunForDefinition(t, f, def, mustFingerprint(t, def))

	future := domain.SchemaVersion + 1
	forgeSchemaVersion(t, f, runID, future)

	_, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if !errors.Is(err, domain.ErrMigrationRefused) {
		t.Fatalf("Resume() of newer state error = %v, want ErrMigrationRefused", err)
	}
	hint := domain.HintOf(err)
	if hint == "" {
		t.Fatal("the refusal carries no hint; the operator cannot act on it")
	}
	if !strings.Contains(hint, strconv.Itoa(future)) {
		t.Errorf("hint %q does not name the schema version that wrote the state", hint)
	}

	refusals := eventsOfType(eventsOf(t, f, runID), string(domain.EventStateMigrationRefused))
	if len(refusals) != 1 {
		t.Fatalf("state.migration_refused events = %d, want exactly 1", len(refusals))
	}
	payload := payloadOf(t, refusals[0])
	if got := payload["run_schema_version"]; got != float64(future) {
		t.Errorf("refused run_schema_version = %v, want %d", got, future)
	}
	if got := payload["store_schema_version"]; got != float64(domain.SchemaVersion) {
		t.Errorf("refused store_schema_version = %v, want %d", got, domain.SchemaVersion)
	}

	run, err := f.store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Status != domain.RunPending {
		t.Errorf("status after a refused resume = %q, want pending (untouched)", run.Status)
	}

	// Recording the refusal took the lease; a refusal must not block the operator it
	// just refused, so the claim is released before the error is returned.
	lease, err := f.store.AcquireLease(context.Background(), runID, "operator-after-refusal", time.Minute)
	if err != nil {
		t.Fatalf("lease after a refused resume error = %v, want the run to be free", err)
	}
	if err := f.store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
}

// forgeSchemaVersion rewrites the schema version recorded on a run, which is how a
// record written by a different build presents itself. Nothing else can produce it:
// CreateRun always stamps this build's version.
func forgeSchemaVersion(t *testing.T, f *fixture, runID uuid.UUID, version int) {
	t.Helper()

	if _, err := f.store.DB().ExecContext(context.Background(),
		"UPDATE runs SET schema_version = ? WHERE id = ?", version, runID.String()); err != nil {
		t.Fatalf("forge schema version: %v", err)
	}
}

// payloadOf decodes an event's canonical JSON payload for assertions.
func payloadOf(t *testing.T, event domain.Event) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", event.Type, err)
	}
	return payload
}
