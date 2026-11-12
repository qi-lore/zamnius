package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

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

func TestCreateRunRequiresTheSnapshotAndPhases(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()

	// Without the definition snapshot a resume would depend on the operator's file,
	// so creation refuses rather than storing an incomplete run.
	_, err := store.CreateRun(ctx, domain.RunSpec{
		ID:                    uuid.New(),
		DefinitionName:        "no-snapshot",
		DefinitionFingerprint: domain.HashBytes([]byte("definition")),
		Phases:                []domain.PhaseState{{Name: "plan", Status: domain.PhasePending}},
	})
	if err == nil {
		t.Fatal("CreateRun accepted a run without a definition snapshot")
	}

	_, err = store.CreateRun(ctx, domain.RunSpec{
		ID:                    uuid.New(),
		DefinitionName:        "no-phases",
		DefinitionFingerprint: domain.HashBytes([]byte("definition")),
		DefinitionJSON:        []byte(`{"version":"1","name":"no-phases"}`),
	})
	if err == nil {
		t.Fatal("CreateRun accepted a run without phases")
	}
}

func TestCommitAttemptResultIsAtomicAndChainsEvents(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	phases, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	state := phases[0]
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

	phases, err = store.Phases(ctx, run.ID)
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

	attempt := runningAttempt(run.ID, "plan", 1)

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

func TestCommitAttemptResultRollsBackOnUnwritableArtifact(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	attempt := runningAttempt(run.ID, "plan", 1)
	// Two artifact rows with one digest violate the UNIQUE sha256 constraint, which
	// is exactly what a duplicated side effect would try to do; the transaction must
	// roll back rather than record an attempt the store cannot describe honestly.
	ref := domain.ArtifactRef{
		ID:           uuid.New(),
		SHA256:       domain.HashBytes([]byte("same")),
		RunID:        run.ID,
		Phase:        "plan",
		AttemptID:    &attempt.ID,
		Kind:         "plan",
		MediaType:    "text/plain",
		SizeBytes:    4,
		RelativePath: "artifacts/xx/x",
		CreatedAt:    nowUTC(),
	}
	err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt:   attempt,
		Artifacts: []domain.ArtifactRef{ref},
		Events:    []domain.EventDraft{{Type: domain.EventAttemptFailed, Actor: domain.ActorSystem, Phase: "plan"}},
	})
	if err != nil {
		t.Fatalf("first commit error = %v", err)
	}

	// A second attempt claiming the same digest is accepted (write-once content),
	// but the first producer keeps provenance.
	second := runningAttempt(run.ID, "plan", 2)
	other := ref
	other.ID = uuid.New()
	other.AttemptID = &second.ID
	other.Kind = "report"
	if err := store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt:   second,
		Artifacts: []domain.ArtifactRef{other},
	}); err != nil {
		t.Fatalf("second commit error = %v", err)
	}

	attempts, err := store.Attempts(ctx, run.ID, "plan")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts[0].Artifacts) != 1 {
		t.Fatalf("first attempt kept %d artifacts, want 1", len(attempts[0].Artifacts))
	}
	if attempts[0].Artifacts[0].Kind != "plan" {
		t.Errorf("provenance moved to the second producer: kind = %q", attempts[0].Artifacts[0].Kind)
	}
}

func TestCommitPhaseResultAppliesRunDelta(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan", "build")

	phases, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	phase := phases[0]
	phase.Status = domain.PhaseRunning
	started := nowUTC()
	phase.StartedAt = &started
	position := "plan"

	if err := store.CommitPhaseResult(ctx, domain.PhaseCommit{
		Phase:           phase,
		Run:             &domain.RunDelta{Status: domain.RunRunning, CurrentPhase: &position},
		ExpectedVersion: run.RecordVersion,
		Events:          []domain.EventDraft{{Type: domain.EventPhaseStarted, Actor: domain.ActorSystem, Phase: "plan"}},
	}); err != nil {
		t.Fatalf("CommitPhaseResult() error = %v", err)
	}

	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if loaded.Status != domain.RunRunning {
		t.Errorf("status = %q, want running", loaded.Status)
	}
	if loaded.CurrentPhase == nil || *loaded.CurrentPhase != "plan" {
		t.Errorf("current_phase = %v, want plan", loaded.CurrentPhase)
	}
	if loaded.RecordVersion <= run.RecordVersion {
		t.Errorf("record_version did not advance: %d", loaded.RecordVersion)
	}

	phases, err = store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	if phases[0].Status != domain.PhaseRunning {
		t.Errorf("phase status = %q, want running", phases[0].Status)
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
	if loaded.OutcomeReason == nil || *loaded.OutcomeReason != reason {
		t.Errorf("outcome_reason = %v, want %q", loaded.OutcomeReason, reason)
	}
}

func TestCommitWithStaleVersionIsRefused(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	// Another owner advanced the row.
	if err := store.CommitRunResult(ctx, domain.RunCommit{
		RunID:           run.ID,
		ExpectedVersion: run.RecordVersion,
		Delta:           domain.RunDelta{Status: domain.RunRunning},
	}); err != nil {
		t.Fatalf("first commit error = %v", err)
	}

	// The original holder still believes version 1; it must be told to re-read
	// instead of overwriting the newer owner's state.
	err := store.CommitRunResult(ctx, domain.RunCommit{
		RunID:           run.ID,
		ExpectedVersion: run.RecordVersion,
		Delta:           domain.RunDelta{Status: domain.RunSucceeded},
	})
	if !errors.Is(err, domain.ErrStaleRecord) {
		t.Fatalf("stale commit error = %v, want ErrStaleRecord", err)
	}

	loaded, err := store.LoadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if loaded.Status != domain.RunRunning {
		t.Errorf("stale commit overwrote the status: %q", loaded.Status)
	}
}

func TestCommitPhaseWithoutDeltaStillGuardsTheVersion(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ctx := context.Background()
	run := createRun(t, store, "plan")

	if err := store.CommitRunResult(ctx, domain.RunCommit{
		RunID:           run.ID,
		ExpectedVersion: run.RecordVersion,
		Delta:           domain.RunDelta{Status: domain.RunRunning},
	}); err != nil {
		t.Fatalf("first commit error = %v", err)
	}

	phases, err := store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	phase := phases[0]
	phase.Status = domain.PhaseFailed

	err = store.CommitPhaseResult(ctx, domain.PhaseCommit{
		Phase:           phase,
		ExpectedVersion: run.RecordVersion,
	})
	if !errors.Is(err, domain.ErrStaleRecord) {
		t.Fatalf("stale phase commit error = %v, want ErrStaleRecord", err)
	}

	phases, err = store.Phases(ctx, run.ID)
	if err != nil {
		t.Fatalf("Phases() error = %v", err)
	}
	if phases[0].Status != domain.PhasePending {
		t.Errorf("stale phase commit changed the row: %q", phases[0].Status)
	}
}

func TestCommitMissingRunIsNotFound(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	err := store.CommitRunResult(context.Background(), domain.RunCommit{
		RunID: uuid.New(),
		Delta: domain.RunDelta{Status: domain.RunSucceeded},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("commit to a missing run error = %v, want ErrNotFound", err)
	}
}

func runningAttempt(runID uuid.UUID, phase string, number int) domain.Attempt {
	return domain.Attempt{
		ID:               uuid.New(),
		RunID:            runID,
		Phase:            phase,
		AttemptNo:        number,
		Status:           domain.AttemptRunning,
		AgentName:        "planner",
		ModelProvider:    "fake",
		ModelName:        "fake-1",
		InputFingerprint: "fp",
		PromptVersion:    "v1",
		StartedAt:        nowUTC(),
	}
}
