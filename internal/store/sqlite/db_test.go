package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/store/sqlite"
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

func TestCreateRunWritesPendingPhasesAndEvent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "alpha", "beta")

	if run.Status != domain.RunPending {
		t.Errorf("status = %q, want pending", run.Status)
	}
	if run.RecordVersion != 1 {
		t.Errorf("record_version = %d, want 1", run.RecordVersion)
	}
	if run.OwnerID != nil || run.LeaseExpiresAt != nil {
		t.Error("a new run must be unowned; the owner/lease CHECK constraint allows only both-null")
	}

	phases, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	if len(phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(phases))
	}
	if phases[0].Name != "alpha" || phases[1].Name != "beta" {
		t.Errorf("phase order = %s,%s; want declared order", phases[0].Name, phases[1].Name)
	}
	if phases[0].Status != domain.PhasePending {
		t.Errorf("phase status = %q, want pending", phases[0].Status)
	}

	events, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != string(domain.EventRunCreated) {
		t.Fatalf("events = %+v, want exactly one run.created", events)
	}
}

func TestCommitAttemptResultIsAtomicAndChainsEvents(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	phase, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	state := phase[0]
	state.Status = domain.PhaseSucceeded
	state.AttemptCount = 1
	ended := nowUTC()
	state.StartedAt = &ended
	state.EndedAt = &ended

	attempt := domain.Attempt{
		ID:               uuid.New(),
		RunID:            run.ID,
		Phase:            "plan",
		AttemptNo:        1,
		Status:           domain.AttemptSucceeded,
		AgentName:        "planner",
		ModelProvider:    "fake",
		ModelName:        "fake-1",
		InputFingerprint: domain.HashBytes([]byte("input")),
		PromptVersion:    "sha256:abc",
		StartedAt:        ended,
		EndedAt:          &ended,
	}
	artifact := domain.ArtifactRef{
		ID:           uuid.New(),
		SHA256:       domain.HashBytes([]byte("# plan\n")),
		RunID:        run.ID,
		Phase:        "plan",
		AttemptID:    &attempt.ID,
		Kind:         "plan",
		MediaType:    "text/markdown",
		SizeBytes:    7,
		RelativePath: "artifacts/ab/abc",
		CreatedAt:    ended,
	}

	err = store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt:   attempt,
		Artifacts: []domain.ArtifactRef{artifact},
		Phase:     &state,
		Events: []domain.EventDraft{
			{Type: domain.EventAttemptStarted, Actor: domain.ActorAgent("planner"), Phase: "plan", AttemptID: attempt.ID,
				Payload: map[string]any{"attempt_no": 1}},
			{Type: domain.EventAttemptSucceeded, Actor: domain.ActorAgent("planner"), Phase: "plan", AttemptID: attempt.ID,
				Payload: map[string]any{"tokens_in": 10, "tokens_out": 20}},
		},
		Decisions: []domain.DecisionDraft{{
			Phase:     "plan",
			AttemptID: attempt.ID,
			Kind:      domain.DecisionJoin,
			Summary:   "single-agent phase completed",
			Rationale: "the only attempt reported success",
			Evidence:  map[string]any{"attempt_id": attempt.ID.String()},
		}},
	})
	if err != nil {
		t.Fatalf("CommitAttemptResult() error = %v", err)
	}

	attempts, err := store.Attempts(ctx, run.ID, "plan")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if len(attempts[0].Artifacts) != 1 || attempts[0].Artifacts[0].SHA256 != artifact.SHA256 {
		t.Fatalf("artifact provenance lost: %+v", attempts[0].Artifacts)
	}

	phases, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	if phases[0].Status != domain.PhaseSucceeded || phases[0].AttemptCount != 1 {
		t.Errorf("phase row not updated: %+v", phases[0])
	}

	events, err := store.Events(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (created, started, succeeded)", len(events))
	}
	// Each event chains to its predecessor; the first has no predecessor.
	if events[0].PrevHash != "" {
		t.Errorf("first event prev_hash = %q, want empty", events[0].PrevHash)
	}
	for i := 1; i < len(events); i++ {
		if events[i].PrevHash != events[i-1].Hash {
			t.Fatalf("event %d does not chain to event %d", i, i-1)
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("seq not increasing: %d then %d", events[i-1].Seq, events[i].Seq)
		}
	}

	decisions, err := store.Decisions(ctx, run.ID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].Kind != domain.DecisionJoin {
		t.Fatalf("decisions = %+v, want one join decision", decisions)
	}
}

func TestCommitAttemptResultRollsBackOnBadEvent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	attempt := domain.Attempt{
		ID:               uuid.New(),
		RunID:            run.ID,
		Phase:            "plan",
		AttemptNo:        1,
		Status:           domain.AttemptRunning,
		AgentName:        "planner",
		ModelProvider:    "fake",
		ModelName:        "fake-1",
		InputFingerprint: "fp",
		PromptVersion:    "v1",
		StartedAt:        nowUTC(),
	}

	// An event outside the vocabulary is a programming error; it must abort the
	// whole commit so a state change is never recorded without its audit record.
	err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt: attempt,
		Events:  []domain.EventDraft{{Type: domain.EventType("phase.exploded"), Actor: domain.ActorSystem}},
	})
	if err == nil {
		t.Fatal("CommitAttemptResult accepted an event outside the vocabulary")
	}

	attempts, err := store.Attempts(ctx, run.ID, "plan")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("aborted commit left %d attempt rows behind", len(attempts))
	}
}

func TestCommitRunResultAccumulatesSpend(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	reason := "all phases succeeded"
	err := store.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Delta: domain.RunDelta{
			Status:        domain.RunSucceeded,
			OutcomeReason: &reason,
			SpentDelta:    domain.Consumption{Attempts: 1, TokensIn: 10, TokensOut: 5},
		},
		Events: []domain.EventDraft{{Type: domain.EventRunSucceeded, Actor: domain.ActorSystem}},
	})
	if err != nil {
		t.Fatalf("CommitRunResult() error = %v", err)
	}
	// A second delta accumulates; two branches must not erase each other's spend.
	err = store.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Delta: domain.RunDelta{SpentDelta: domain.Consumption{Attempts: 2, TokensIn: 7}},
	})
	if err != nil {
		t.Fatalf("second CommitRunResult() error = %v", err)
	}

	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if loaded.Status != domain.RunSucceeded {
		t.Errorf("status = %q, want succeeded", loaded.Status)
	}
	if loaded.Spent.Attempts != 3 || loaded.Spent.TokensIn != 17 || loaded.Spent.TokensOut != 5 {
		t.Errorf("spend = %+v, want accumulated 3/17/5", loaded.Spent)
	}
	if loaded.RecordVersion <= run.RecordVersion {
		t.Errorf("record_version did not advance: %d", loaded.RecordVersion)
	}
}

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
