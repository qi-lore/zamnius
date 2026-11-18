package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qi-lore/zamnius/internal/domain"
)

func TestLoadRunMissingIsNotFound(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	_, err := store.LoadRun(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("LoadRun returned no error for a missing run")
	}
	if got := domain.ClassOf(err); got != domain.ErrClassPermanent {
		t.Errorf("class = %q, want permanent", got)
	}
	if !isSentinel(err, domain.ErrNotFound) {
		t.Errorf("error %v must wrap ErrNotFound", err)
	}
}

func TestLoadRunRestoresTheSnapshotAndBudget(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if string(loaded.DefinitionJSON) != string(run.DefinitionJSON) {
		t.Errorf("definition snapshot = %s, want %s", loaded.DefinitionJSON, run.DefinitionJSON)
	}
	if loaded.DefinitionFingerprint != run.DefinitionFingerprint {
		t.Errorf("fingerprint = %q, want %q", loaded.DefinitionFingerprint, run.DefinitionFingerprint)
	}
	if loaded.SchemaVersion != domain.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", loaded.SchemaVersion, domain.SchemaVersion)
	}
	if loaded.CreatedAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Error("timestamps not restored")
	}
}

func TestEventsRespectFromSeq(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	for i := 0; i < 3; i++ {
		if err := store.CommitRunResult(ctx, domain.RunCommit{
			RunID:  run.ID,
			Delta:  domain.RunDelta{},
			Events: []domain.EventDraft{{Type: domain.EventRunResumed, Actor: domain.ActorSystem}},
		}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	all, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events(0) error = %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("events = %d, want 4", len(all))
	}

	// The resume protocol reads forward from the last consumed seq, so the cursor
	// must be exclusive and stable.
	rest, err := store.Events(ctx, run.ID, all[1].Seq)
	if err != nil {
		t.Fatalf("Events(%d) error = %v", all[1].Seq, err)
	}
	if len(rest) != 2 {
		t.Fatalf("events after seq %d = %d, want 2", all[1].Seq, len(rest))
	}
	if rest[0].Seq != all[2].Seq {
		t.Errorf("first event after cursor = %d, want %d", rest[0].Seq, all[2].Seq)
	}
	for i := range rest {
		if rest[i].RunID != run.ID {
			t.Errorf("event %d belongs to %s", i, rest[i].RunID)
		}
	}
}

func TestEventsPreservePayloadMetadata(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	attemptID := uuid.New()
	if err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt: runningAttempt(run.ID, "plan", 1),
		Events: []domain.EventDraft{{
			Type:      domain.EventAttemptFailed,
			Actor:     domain.ActorAgent("planner"),
			Phase:     "plan",
			AttemptID: attemptID,
			Payload:   map[string]any{"error_class": "transient", "detail": "429"},
		}},
	}); err != nil {
		t.Fatalf("CommitAttemptResult() error = %v", err)
	}

	events, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	failed := events[len(events)-1]
	if failed.Type != string(domain.EventAttemptFailed) {
		t.Fatalf("type = %q, want attempt.failed", failed.Type)
	}
	if failed.Actor != domain.ActorAgent("planner") {
		t.Errorf("actor = %q", failed.Actor)
	}
	if failed.Phase == nil || *failed.Phase != "plan" {
		t.Errorf("phase = %v, want plan", failed.Phase)
	}
	if failed.AttemptID == nil || *failed.AttemptID != attemptID {
		t.Errorf("attempt id = %v, want %s", failed.AttemptID, attemptID)
	}
	if failed.TS.IsZero() {
		t.Error("timestamp not restored")
	}

	// Payloads are stored canonical, so a reader can decode without guessing.
	var payload struct {
		ErrorClass string `json:"error_class"`
		Detail     string `json:"detail"`
	}
	if err := json.Unmarshal(failed.Payload, &payload); err != nil {
		t.Fatalf("payload is not decodable JSON: %v (%s)", err, failed.Payload)
	}
	if payload.ErrorClass != "transient" || payload.Detail != "429" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestAttemptsAreOrderedAndCarryArtifacts(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan", "build")

	for _, number := range []int{2, 1} {
		attempt := runningAttempt(run.ID, "plan", number)
		content := []byte{byte('a' + number)}
		ref := domain.ArtifactRef{
			ID:           uuid.New(),
			SHA256:       domain.HashBytes(content),
			RunID:        run.ID,
			Phase:        "plan",
			AttemptID:    &attempt.ID,
			Kind:         "plan",
			MediaType:    "text/plain",
			SizeBytes:    int64(len(content)),
			RelativePath: "artifacts/xx/x",
			CreatedAt:    nowUTC(),
		}
		if err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
			Attempt:   attempt,
			Artifacts: []domain.ArtifactRef{ref},
		}); err != nil {
			t.Fatalf("commit attempt %d: %v", number, err)
		}
	}

	attempts, err := store.Attempts(ctx, run.ID, "plan")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	// Attempt order is the retry order the audit reads.
	if attempts[0].AttemptNo != 1 || attempts[1].AttemptNo != 2 {
		t.Fatalf("attempt order = %d,%d; want ascending", attempts[0].AttemptNo, attempts[1].AttemptNo)
	}
	for i, attempt := range attempts {
		if len(attempt.Artifacts) != 1 {
			t.Fatalf("attempt %d has %d artifacts, want 1", attempt.AttemptNo, len(attempt.Artifacts))
		}
		if attempt.Artifacts[0].AttemptID == nil || *attempt.Artifacts[0].AttemptID != attempt.ID {
			t.Errorf("attempt %d artifact provenance = %v", i, attempt.Artifacts[0].AttemptID)
		}
	}

	// A phase with no attempts reads as an empty list, not an error: resume uses
	// this to decide whether a phase ever started.
	buildAttempts, err := store.Attempts(ctx, run.ID, "build")
	if err != nil {
		t.Fatalf("Attempts(build) error = %v", err)
	}
	if len(buildAttempts) != 0 {
		t.Errorf("build attempts = %d, want 0", len(buildAttempts))
	}
}

func TestArtifactsListEveryDigestOfTheRun(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	digests := make(map[string]struct{})
	for i := 0; i < 2; i++ {
		attempt := runningAttempt(run.ID, "plan", i+1)
		content := []byte{byte('0' + i)}
		digest := domain.HashBytes(content)
		digests[digest] = struct{}{}
		ref := domain.ArtifactRef{
			ID:           uuid.New(),
			SHA256:       digest,
			RunID:        run.ID,
			Phase:        "plan",
			AttemptID:    &attempt.ID,
			Kind:         "plan",
			MediaType:    "text/plain",
			SizeBytes:    1,
			RelativePath: "artifacts/xx/x",
			CreatedAt:    nowUTC(),
		}
		if err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
			Attempt:   attempt,
			Artifacts: []domain.ArtifactRef{ref},
		}); err != nil {
			t.Fatalf("commit attempt %d: %v", i, err)
		}
	}

	refs, err := store.Artifacts(ctx, run.ID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(refs))
	}
	for _, ref := range refs {
		if _, ok := digests[ref.SHA256]; !ok {
			t.Errorf("unexpected digest %s", ref.SHA256)
		}
		if ref.CreatedAt.IsZero() {
			t.Error("artifact timestamp not restored")
		}
	}

	known, err := store.AllArtifactSHA256(ctx)
	if err != nil {
		t.Fatalf("AllArtifactSHA256() error = %v", err)
	}
	if len(known) != 2 {
		t.Fatalf("known digests = %d, want 2", len(known))
	}
}

func TestDecisionsAreOrderedByCommit(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	kinds := []domain.DecisionKind{domain.DecisionJoin, domain.DecisionRetry, domain.DecisionBudgetStop}
	for i, kind := range kinds {
		attempt := runningAttempt(run.ID, "plan", i+1)
		if err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
			Attempt: attempt,
			Decisions: []domain.DecisionDraft{{
				Phase:     "plan",
				AttemptID: attempt.ID,
				Kind:      kind,
				Summary:   "summary",
				Rationale: "rationale",
				Evidence:  map[string]any{"attempt_no": i + 1},
			}},
		}); err != nil {
			t.Fatalf("commit decision %d: %v", i, err)
		}
	}

	decisions, err := store.Decisions(ctx, run.ID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	if len(decisions) != len(kinds) {
		t.Fatalf("decisions = %d, want %d", len(decisions), len(kinds))
	}
	for i, decision := range decisions {
		if decision.Kind != kinds[i] {
			t.Errorf("decision %d kind = %q, want %q", i, decision.Kind, kinds[i])
		}
		if decision.Phase == nil || *decision.Phase != "plan" {
			t.Errorf("decision %d phase = %v", i, decision.Phase)
		}
		if decision.AttemptID == nil {
			t.Errorf("decision %d lost its attempt link", i)
		}
		if len(decision.Evidence) == 0 {
			t.Errorf("decision %d evidence is empty", i)
		}
	}
}

func TestReadsAreScopedToTheirRun(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	first := createRun(t, store, "plan")
	second := createRun(t, store, "plan")

	if err := store.CommitRunResult(ctx, domain.RunCommit{
		RunID:  first.ID,
		Delta:  domain.RunDelta{},
		Events: []domain.EventDraft{{Type: domain.EventRunResumed, Actor: domain.ActorSystem}},
		Decisions: []domain.DecisionDraft{{
			Kind: domain.DecisionSkip, Summary: "s", Rationale: "r",
		}},
	}); err != nil {
		t.Fatalf("commit to first run: %v", err)
	}

	events, err := store.Events(ctx, second.ID, 0)
	if err != nil {
		t.Fatalf("Events(second) error = %v", err)
	}
	// Only the second run's own run.created event; a shared event table must not
	// leak another run's history into this reconstruction.
	if len(events) != 1 || events[0].RunID != second.ID {
		t.Fatalf("events for second run = %+v", events)
	}

	decisions, err := store.Decisions(ctx, second.ID)
	if err != nil {
		t.Fatalf("Decisions(second) error = %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions leaked between runs: %+v", decisions)
	}
}

// TestLoadRunToleratesALegacyTimestamp proves the read path accepts a timestamp
// written in RFC3339Nano — an older build's format, or a hand-edited row — even
// though the canonical layout is fixed-width, and refuses a genuinely unparseable
// value rather than returning a zero time.
func TestLoadRunToleratesALegacyTimestamp(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	// RFC3339Nano without fractional seconds: valid, but not the canonical layout.
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE runs SET created_at = ? WHERE id = ?`, "2026-11-12T09:00:00Z", run.ID.String()); err != nil {
		t.Fatalf("seed legacy timestamp: %v", err)
	}
	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v, want the legacy timestamp tolerated", err)
	}
	want := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if !loaded.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, want)
	}

	// Neither layout parses this, so the read must fail with the value named.
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE runs SET created_at = ? WHERE id = ?`, "not-a-timestamp", run.ID.String()); err != nil {
		t.Fatalf("seed unparseable timestamp: %v", err)
	}
	if _, err := store.LoadRun(ctx, run.ID); err == nil {
		t.Fatal("LoadRun() accepted an unparseable timestamp; it must refuse")
	}
}

func isSentinel(err, target error) bool { return errors.Is(err, target) }
