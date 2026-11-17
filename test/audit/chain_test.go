package audit_test

// T054 — chain integrity (US3 scenario 3). Verification must pass on a clean run,
// name the exact `seq` of a tampered payload, hash, or prev_hash, notice a deleted
// event as a broken link, and never depend on the application to enforce the
// append-only rule: the schema enforces it as a second line of defence.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prompt-fu/zamnius/internal/audit"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
)

// TestVerifyChainAcceptsACleanRun is the baseline: a log this build wrote chains
// cleanly, so verification is a real check rather than a constant failure, and the
// links are genuinely present rather than merely unchecked.
func TestVerifyChainAcceptsACleanRun(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	events := f.events(run.RunID)

	if len(events) < 8 {
		t.Fatalf("clean run wrote %d events, want a log long enough to verify", len(events))
	}
	if problems := audit.VerifyChain(events); len(problems) != 0 {
		t.Fatalf("VerifyChain() reported %d problem(s) on a clean run: %+v", len(problems), problems)
	}

	// Each event chains to the stored hash of its predecessor.
	for i, event := range events {
		if i == 0 {
			if event.PrevHash != "" {
				t.Errorf("first event prev_hash = %q, want empty", event.PrevHash)
			}
			continue
		}
		if event.PrevHash != events[i-1].Hash {
			t.Errorf("seq %d prev_hash = %q, want %q (seq %d)",
				event.Seq, event.PrevHash, events[i-1].Hash, events[i-1].Seq)
		}
	}

	// Engine.Verify agrees and reports the counts even though the record is clean.
	report, err := f.engine.Verify(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.OK() {
		t.Errorf("Verify() reported problems on a clean run: %+v", report.Problems)
	}
	if report.Events != len(events) {
		t.Errorf("report.Events = %d, want %d", report.Events, len(events))
	}
	if report.Artifacts == 0 {
		t.Error("report.Artifacts = 0, want the committed artifacts to be counted")
	}
}

// TestVerifyChainReportsATamperedEventAtItsSeq proves a single edit is caught and
// attributed to the exact seq it was made at, so an operator can find the altered
// record instead of merely learning that the chain is broken.
func TestVerifyChainReportsATamperedEventAtItsSeq(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	clean := f.events(run.RunID)

	mutations := []struct {
		name   string
		mutate func(events []domain.Event) int64 // mutates in place, returns the seq changed
	}{
		{
			name: "payload",
			mutate: func(events []domain.Event) int64 {
				event := &events[len(events)/2]
				event.Payload = json.RawMessage(`{"tampered":true}`)
				return event.Seq
			},
		},
		{
			name: "hash",
			mutate: func(events []domain.Event) int64 {
				event := &events[len(events)/2]
				event.Hash = strings.Repeat("0", 64)
				return event.Seq
			},
		},
		{
			name: "prev_hash",
			mutate: func(events []domain.Event) int64 {
				event := &events[len(events)/2]
				event.PrevHash = strings.Repeat("f", 64)
				return event.Seq
			},
		},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			events := append([]domain.Event(nil), clean...)
			seq := mutation.mutate(events)

			problems := audit.VerifyChain(events)
			if len(problems) == 0 {
				t.Fatalf("VerifyChain() accepted a mutated %s at seq %d", mutation.name, seq)
			}
			if !hasChainProblemAtSeq(problems, seq) {
				t.Errorf("no chain problem reported at seq %d: %+v", seq, problems)
			}
			for _, problem := range problems {
				if problem.Kind != audit.ProblemChain {
					t.Errorf("problem kind = %q, want %q", problem.Kind, audit.ProblemChain)
				}
				if problem.Detail == "" {
					t.Errorf("problem at seq %d has no detail", problem.Seq)
				}
			}
		})
	}
}

// TestVerifyChainReportsADeletedEventAsAGap proves removal leaves evidence too: with
// an event gone, its successor's prev_hash points at a hash no surviving event
// carries, so the chain reports the break instead of silently accepting a shorter log.
func TestVerifyChainReportsADeletedEventAsAGap(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	events := f.events(run.RunID)

	index := len(events) / 2
	removed := events[index]
	successor := events[index+1]
	gapped := append(append([]domain.Event(nil), events[:index]...), events[index+1:]...)

	problems := audit.VerifyChain(gapped)
	if len(problems) == 0 {
		t.Fatalf("VerifyChain() accepted a log with seq %d removed", removed.Seq)
	}
	if !hasChainProblemAtSeq(problems, successor.Seq) {
		t.Errorf("no chain problem at seq %d (the event after the removed seq %d): %+v",
			successor.Seq, removed.Seq, problems)
	}
}

// TestVerifyRejectsEventMutationAtTheSchemaLevel pins US3 scenario 3: the
// append-only rule is enforced by the database, not by a convention the application
// could skip, so a writer holding the file still cannot rewrite history — and the
// record is provably unchanged after the rejected attempts.
func TestVerifyRejectsEventMutationAtTheSchemaLevel(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())

	before := f.events(run.RunID)
	if len(before) == 0 {
		t.Fatal("clean run wrote no events")
	}
	ctx := context.Background()
	target := before[len(before)/2]

	if _, err := f.store.DB().ExecContext(ctx,
		"UPDATE events SET payload = ? WHERE seq = ?", `{"tampered":true}`, target.Seq); err == nil {
		t.Fatal("UPDATE on events succeeded; the schema must reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE error = %v, want it to name the append-only rule", err)
	}

	if _, err := f.store.DB().ExecContext(ctx,
		"DELETE FROM events WHERE seq = ?", target.Seq); err == nil {
		t.Fatal("DELETE on events succeeded; the schema must reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE error = %v, want it to name the append-only rule", err)
	}

	after := f.events(run.RunID)
	if len(after) != len(before) {
		t.Errorf("event count = %d after rejected mutations, want %d", len(after), len(before))
	}
	if problems := audit.VerifyChain(after); len(problems) != 0 {
		t.Errorf("VerifyChain() after rejected mutations = %+v, want clean", problems)
	}
}

// hasChainProblemAtSeq reports whether verification flagged a chain break at seq.
func hasChainProblemAtSeq(problems []audit.Problem, seq int64) bool {
	for _, problem := range problems {
		if problem.Seq == seq && problem.Kind == audit.ProblemChain {
			return true
		}
	}
	return false
}

// TestVerifyChainStreamsTheLogOneEventAtATime pins the streaming property research.md
// R12 asks for: the chain is recomputed event by event, each event decided from its own
// content and its predecessor's digest, so a verifier fed the log one Add at a time
// reaches exactly the verdict the slice form does and holds no set of every digest. A
// hole is therefore caught from the survivors alone, and the seq gap names the deletion
// rather than leaving the operator to infer it.
func TestVerifyChainStreamsTheLogOneEventAtATime(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	events := f.events(run.RunID)

	clean := streamChain(events)
	if clean.Count() != len(events) {
		t.Errorf("streamed count = %d, want %d", clean.Count(), len(events))
	}
	if problems, want := clean.Problems(), audit.VerifyChain(events); !reflect.DeepEqual(problems, want) {
		t.Errorf("streamed problems = %+v, want the slice form's %+v", problems, want)
	}
	if len(clean.Problems()) != 0 {
		t.Fatalf("a clean run streamed to %+v, want no problems", clean.Problems())
	}

	// A tampered event is caught from the event and its predecessor alone: the
	// verifier never needed the rest of the log to decide it.
	tampered := append([]domain.Event(nil), events...)
	victim := &tampered[len(tampered)/2]
	victim.Payload = json.RawMessage(`{"tampered":true}`)
	streamed := streamChain(tampered)
	if problems, want := streamed.Problems(), audit.VerifyChain(tampered); !reflect.DeepEqual(problems, want) {
		t.Errorf("streamed problems = %+v, want the slice form's %+v", problems, want)
	}
	if !hasChainProblemAtSeq(streamed.Problems(), victim.Seq) {
		t.Errorf("the streamed verifier missed the tampered seq %d: %+v", victim.Seq, streamed.Problems())
	}

	// A hole is caught with no digest set to consult: the surviving successor does
	// not chain to its predecessor, and the seq gap names the deleted record.
	index := len(events) / 2
	removed := events[index]
	successor := events[index+1]
	gapped := append(append([]domain.Event(nil), events[:index]...), events[index+1:]...)
	hole := streamChain(gapped)
	if hole.Count() != len(gapped) {
		t.Errorf("streamed count over the gapped log = %d, want %d", hole.Count(), len(gapped))
	}
	problem := chainProblemAtSeq(hole.Problems(), successor.Seq)
	if problem == nil {
		t.Fatalf("the streamed verifier missed the gap at seq %d: %+v", successor.Seq, hole.Problems())
	}
	if !strings.Contains(problem.Detail, "gap") {
		t.Errorf("gap detail %q does not name the seq gap left by removing seq %d", problem.Detail, removed.Seq)
	}
}

// streamChain folds a log through a fresh ChainVerifier, one event at a time.
func streamChain(events []domain.Event) *audit.ChainVerifier {
	verifier := audit.NewChainVerifier()
	for i := range events {
		verifier.Add(events[i])
	}
	return verifier
}

// chainProblemAtSeq returns the chain problem reported at a seq, if any.
func chainProblemAtSeq(problems []audit.Problem, seq int64) *audit.Problem {
	for i := range problems {
		if problems[i].Kind == audit.ProblemChain && problems[i].Seq == seq {
			return &problems[i]
		}
	}
	return nil
}

// TestResumeRefusesABrokenChainWithoutRerunning closes the residual gap T046 left: the
// `failOnIntegrity` path on a broken *chain*, since the artifact path is covered by
// T055. The schema rejects UPDATE and DELETE on `events`, so the tamper is forged with
// an INSERT — a successor that claims to chain from the last committed event but whose
// recorded hash is not the digest of its own content, exactly what a rewritten row
// would leave behind. Resume must refuse with ErrIntegrity, fail the run, execute no
// attempt, keep the forged row as evidence, and be a terminal no-op the second time.
func TestResumeRefusesABrokenChainWithoutRerunning(t *testing.T) {
	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()
	ctx := context.Background()

	before := f.events(run.RunID)
	if len(before) == 0 {
		t.Fatal("the paused run wrote no events")
	}
	attemptsBefore := f.attemptCount(run.RunID)
	last := before[len(before)-1]

	// The forged successor chains from the last real event, so the only break is its
	// own hash against its recomputation — the signature of an edited row.
	if _, err := f.store.DB().ExecContext(ctx,
		`INSERT INTO events (run_id, ts, type, actor, phase, attempt_id, payload, prev_hash, hash)
		 VALUES (?, ?, ?, ?, NULL, NULL, ?, ?, ?)`,
		run.RunID.String(),
		domain.FormatTimestamp(last.TS.Add(time.Millisecond)),
		string(domain.EventRunPaused),
		string(domain.ActorSystem),
		`{"forged":true}`,
		last.Hash,
		strings.Repeat("b", 64),
	); err != nil {
		t.Fatalf("forge an event: %v", err)
	}

	after := f.events(run.RunID)
	if len(after) != len(before)+1 {
		t.Fatalf("the forged row is not visible: %d events, want %d", len(after), len(before)+1)
	}
	forged := after[len(after)-1]
	if problems := audit.VerifyChain(after); !hasChainProblemAtSeq(problems, forged.Seq) {
		t.Fatalf("VerifyChain() did not flag the forged seq %d: %+v", forged.Seq, problems)
	}

	result, resumeErr := f.engine.Resume(ctx, run.RunID, engine.ResumeOptions{})
	if resumeErr == nil {
		t.Fatal("Resume() accepted a run whose event chain is broken")
	}
	if !errors.Is(resumeErr, domain.ErrIntegrity) {
		t.Errorf("Resume() error = %v, want it to wrap ErrIntegrity", resumeErr)
	}
	if result.Status != domain.RunFailed {
		t.Errorf("resumed status = %q, want failed", result.Status)
	}
	if hint := domain.HintOf(resumeErr); !strings.Contains(hint, "hash mismatch") {
		t.Errorf("resume hint %q does not carry the chain evidence", hint)
	}

	final := f.events(run.RunID)
	if added := eventTypesFrom(final, len(after)); len(added) != 1 || added[0] != string(domain.EventRunFailed) {
		t.Errorf("refused resume appended %v, want exactly [%s]", added, domain.EventRunFailed)
	}
	if got := f.attemptCount(run.RunID); got != attemptsBefore {
		t.Errorf("attempt count = %d after the refusal, want %d (no work re-executed)", got, attemptsBefore)
	}
	// The forged row is evidence, not something resume repairs: the record keeps the
	// break it refuted.
	if got := len(final); got != len(after)+1 {
		t.Errorf("event count = %d after the refusal, want %d (only run.failed appended)", got, len(after)+1)
	}
	if problems := audit.VerifyChain(final); !hasChainProblemAtSeq(problems, forged.Seq) {
		t.Errorf("the forged seq %d is gone after the refusal: %+v", forged.Seq, problems)
	}

	// A second resume changes nothing: the run is terminal (FR-018).
	stable := len(f.events(run.RunID))
	if _, err := f.engine.Resume(ctx, run.RunID, engine.ResumeOptions{}); err != nil {
		t.Fatalf("second Resume() error = %v, want a terminal-run no-op", err)
	}
	if got := len(f.events(run.RunID)); got != stable {
		t.Errorf("second resume appended %d event(s), want no change", got-stable)
	}
}
