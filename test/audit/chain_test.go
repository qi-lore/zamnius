package audit_test

// T054 — chain integrity (US3 scenario 3). Verification must pass on a clean run,
// name the exact `seq` of a tampered payload, hash, or prev_hash, notice a deleted
// event as a broken link, and never depend on the application to enforce the
// append-only rule: the schema enforces it as a second line of defence.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prompt-fu/zamnius/internal/audit"
	"github.com/prompt-fu/zamnius/internal/domain"
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
