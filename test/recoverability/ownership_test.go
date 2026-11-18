package recoverability_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/test/fakes"
)

// This file covers the ownership and idempotence guarantees of User Story 2
// (T045): the lease admits exactly one owner across processes, resuming a terminal
// run is a no-op that changes nothing, taking a run over after its owner's lease
// lapsed is recorded with the previous owner, and a run whose recorded definition
// no longer matches the one it would execute is refused unless the operator
// overrides the drift on the record.

// TestConcurrentLeaseAcquisitionHasExactlyOneOwner proves the lease is exclusive
// across two instances over one state directory — two processes in production —
// and that the loser is told the run belongs to another live process rather than
// silently proceeding to write it (FR-016, ADR-0004, contracts/library-api.md §4.2).
func TestConcurrentLeaseAcquisitionHasExactlyOneOwner(t *testing.T) {
	dir := t.TempDir()
	def := twoPhaseDefinition(t)

	// Two instances over one state directory: two stores on one database file, which
	// is the only way the conditional UPDATE can be exercised as it is in production.
	first := openInstance(t, dir, "owner-first", &answerer{}, fakes.NewIDSource())
	second := openInstance(t, dir, "owner-second", &answerer{}, fakes.NewIDSource())

	// An unowned, non-terminal run: both instances are entitled to try, so the
	// single-owner outcome has to come from the lease itself, not from the run state.
	runID := createRunForDefinition(t, first, def, mustFingerprint(t, def))

	contenders := []struct {
		owner string
		f     *fixture
	}{
		{owner: "owner-first", f: first},
		{owner: "owner-second", f: second},
	}

	type outcome struct {
		owner string
		err   error
	}

	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan outcome, len(contenders))

	var wg sync.WaitGroup
	for _, contender := range contenders {
		contender := contender
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := contender.f.store.AcquireLease(ctx, runID, contender.owner, time.Minute)
			results <- outcome{owner: contender.owner, err: err}
		}()
	}
	// Release both contenders together so the acquisition is genuinely concurrent
	// rather than a race decided by goroutine start order.
	close(start)
	wg.Wait()
	close(results)

	winners := make([]string, 0, 1)
	for result := range results {
		if result.err == nil {
			winners = append(winners, result.owner)
			continue
		}
		if !errors.Is(result.err, domain.ErrRunAlreadyOwned) {
			t.Errorf("contender %s failed with %v, want ErrRunAlreadyOwned", result.owner, result.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("lease winners = %v, want exactly one", winners)
	}

	loaded, err := first.store.LoadRun(ctx, runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if loaded.OwnerID == nil || *loaded.OwnerID != winners[0] {
		t.Errorf("recorded owner = %v, want the single winner %q", loaded.OwnerID, winners[0])
	}
}

// TestResumeOfTerminalRunIsANoOp proves an idempotent resume (FR-018,
// contracts/library-api.md §4.1): resuming a run that already succeeded returns the
// terminal status with no error and appends nothing to the record. This is the
// "already complete, exit 0" behaviour at the library boundary.
func TestResumeOfTerminalRunIsANoOp(t *testing.T) {
	dir := t.TempDir()

	control, runID := runControl(t, dir, &answerer{}, twoPhaseDefinition(t))
	before := eventsOf(t, control, runID)
	if len(before) == 0 {
		t.Fatal("the control run recorded no events; the no-op assertion would be vacuous")
	}

	// A fresh instance, as a later process would be, resumes the terminal run.
	model := &answerer{}
	resumed := openInstance(t, dir, "owner-later", model, fakes.NewIDSource())

	result, err := resumed.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() on a terminal run error = %v, want nil", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}

	after := eventsOf(t, resumed, runID)
	if len(after) != len(before) {
		t.Errorf("events after resuming a terminal run = %d, want the %d already recorded", len(after), len(before))
	}
	for _, event := range after {
		if event.Type == string(domain.EventRunResumed) {
			t.Error("resuming a terminal run appended run.resumed; a terminal run must be immutable")
		}
	}
	// Nothing was executed either: the model was never called.
	if got := model.callsFor("draft"); got != 0 {
		t.Errorf("resuming a terminal run called the model %d times, want 0", got)
	}
}

// TestResumeAfterLeaseExpiryRecordsTakeover proves ownership passes cleanly when a
// process dies without releasing: a later instance takes over the lapsed lease and
// the record names the previous owner, so the audit trail explains why two owners
// advanced one run (FR-016, ADR-0004, US2).
func TestResumeAfterLeaseExpiryRecordsTakeover(t *testing.T) {
	dir := t.TempDir()
	def := twoPhaseDefinition(t)

	first := openInstance(t, dir, "owner-first", &answerer{}, fakes.NewIDSource())
	runID := createRunForDefinition(t, first, def, mustFingerprint(t, def))

	ctx := context.Background()
	// The first owner claims the run with a lease that lapses immediately, exactly
	// as a process that died without releasing would leave it.
	if _, err := first.store.AcquireLease(ctx, runID, "owner-first", time.Millisecond); err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	time.Sleep(2 * leaseTTL)

	resumed := openInstance(t, dir, "owner-second", &answerer{}, fakes.NewIDSource())
	result, err := resumed.engine.Resume(ctx, runID, engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume() after lease expiry error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", result.Status)
	}

	events := eventsOf(t, resumed, runID)
	takeovers := eventsOfType(events, string(domain.EventRunTakenOver))
	if len(takeovers) != 1 {
		t.Fatalf("run.taken_over events = %d, want exactly 1", len(takeovers))
	}
	payload := string(takeovers[0].Payload)
	for _, owner := range []string{"owner-first", "owner-second"} {
		if !strings.Contains(payload, owner) {
			t.Errorf("takeover payload %s does not name %s", payload, owner)
		}
	}

	decisions, err := resumed.store.Decisions(ctx, runID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	takeoverDecisions := 0
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionTakeover {
			takeoverDecisions++
			if decision.Rationale == "" {
				t.Error("takeover decision has no rationale explaining the ownership change")
			}
		}
	}
	if takeoverDecisions != 1 {
		t.Errorf("takeover decisions = %d, want exactly 1", takeoverDecisions)
	}
}

// TestResumeRefusesDefinitionDrift proves a resume whose definition fingerprint does
// not match the one the run records is refused with an actionable diagnostic and
// leaves the record untouched, rather than executing a workflow the run was not
// created for (FR-014, data-model.md §10, T049).
//
// The engine derives the definition to execute from the run's stored snapshot, so
// the mismatch it can observe is between the snapshot and the run's recorded
// fingerprint. A run is created here with a fingerprint that deliberately disagrees
// with its snapshot, which is how a definition that drifted between start and resume
// presents itself to the guard.
func TestResumeRefusesDefinitionDrift(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-drift", &answerer{}, fakes.NewIDSource())
	def := twoPhaseDefinition(t)

	stored := domain.HashBytes([]byte("a workflow the operator has since edited"))
	runID := createRunForDefinition(t, f, def, stored)

	before := eventsOf(t, f, runID)

	_, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{})
	if !errors.Is(err, domain.ErrDefinitionDrift) {
		t.Fatalf("Resume() error = %v, want ErrDefinitionDrift", err)
	}

	hint := domain.HintOf(err)
	if hint == "" {
		t.Fatal("drift refusal carries no hint; the operator cannot act on it")
	}
	for _, fingerprint := range []string{stored, mustFingerprint(t, def)} {
		if !strings.Contains(hint, fingerprint) {
			t.Errorf("hint %q does not name fingerprint %s", hint, fingerprint)
		}
	}
	if !strings.Contains(hint, "--allow-definition-drift") {
		t.Errorf("hint %q does not tell the operator how to proceed", hint)
	}

	if after := eventsOf(t, f, runID); len(after) != len(before) {
		t.Errorf("a refused resume appended %d events, want 0", len(after)-len(before))
	}
	run, err := f.store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Status != domain.RunPending {
		t.Errorf("status after a refused resume = %q, want pending (untouched)", run.Status)
	}
}

// TestResumeRecordsDefinitionDriftOverride proves the deliberate override is not
// silent: with --allow-definition-drift the run proceeds and the override is
// recorded as a decision naming both fingerprints, so a reader can tell why a run
// executed a workflow its record did not expect (FR-014, T049).
func TestResumeRecordsDefinitionDriftOverride(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-drift", &answerer{}, fakes.NewIDSource())
	def := twoPhaseDefinition(t)

	stored := domain.HashBytes([]byte("a workflow the operator has since edited"))
	runID := createRunForDefinition(t, f, def, stored)

	result, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{AllowDefinitionDrift: true})
	if err != nil {
		t.Fatalf("Resume() with AllowDefinitionDrift error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status with drift overridden = %q, want succeeded", result.Status)
	}

	supplied := mustFingerprint(t, def)
	decisions, err := f.store.Decisions(context.Background(), runID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}

	overrides := 0
	for _, decision := range decisions {
		if decision.Kind != domain.DecisionResumeRestart || !strings.Contains(decision.Summary, "drift") {
			continue
		}
		overrides++
		for _, fingerprint := range []string{stored, supplied} {
			if !strings.Contains(decision.Rationale, fingerprint) {
				t.Errorf("override rationale %q does not name fingerprint %s", decision.Rationale, fingerprint)
			}
			if !strings.Contains(string(decision.Evidence), fingerprint) {
				t.Errorf("override evidence %s does not name fingerprint %s", decision.Evidence, fingerprint)
			}
		}
	}
	if overrides != 1 {
		t.Errorf("definition-drift override decisions = %d, want exactly 1", overrides)
	}
}

// createRunForDefinition persists a fresh, unowned run for def, recording the given
// fingerprint. Tests use it to start from a known, non-terminal position without
// first driving a whole run, and to present a fingerprint that deliberately
// disagrees with the stored snapshot.
func createRunForDefinition(t *testing.T, f *fixture, def definition.Definition, fingerprint string) uuid.UUID {
	t.Helper()

	snapshot, err := definition.Snapshot(def)
	if err != nil {
		t.Fatalf("snapshot definition: %v", err)
	}
	phases := make([]domain.PhaseState, 0, len(def.Phases))
	for i, phase := range def.Phases {
		phases = append(phases, domain.PhaseState{Name: phase.Name, Ordinal: i, Status: domain.PhasePending})
	}

	runID := fakes.NewIDSource()()
	if _, err := f.store.CreateRun(context.Background(), domain.RunSpec{
		ID:                    runID,
		DefinitionName:        def.Name,
		DefinitionFingerprint: fingerprint,
		DefinitionJSON:        snapshot,
		Phases:                phases,
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return runID
}

// mustFingerprint is the fingerprint the engine computes for def, so a test can
// build a run whose recorded fingerprint agrees or disagrees with it as the case
// requires.
func mustFingerprint(t *testing.T, def definition.Definition) string {
	t.Helper()

	fingerprint, err := definition.Fingerprint(def)
	if err != nil {
		t.Fatalf("fingerprint definition: %v", err)
	}
	return fingerprint
}
