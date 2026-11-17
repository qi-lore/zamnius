package audit_test

// T058 — the audit views (US3). Three views join the reconstruction T057 landed, and
// every one of them is derived from the store on demand and never stored, so a cached
// projection cannot become a second source of truth (Principle III, data-model.md §8):
//
//   - the ordered timeline: events in `seq` order with decision records interleaved at
//     the point the transaction that wrote them put them (contracts/store.md §1);
//   - the run status view `zamnius run status` prints — the run row's fields, the phase
//     rows, the newest events, and the next expected action (contracts/cli.md §1);
//   - the verification view: chain validity plus every artifact fingerprint.
//
// The tests read a real store a real engine wrote, read it twice where a view could
// have cached, and pin the placement rule with hand-built records a store cannot be
// asked to produce on purpose (a decision whose commit wrote no event at all).

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/audit"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// viewScenario is one sampled run, named so a failure says which record shape broke.
type viewScenario struct {
	name string
	run  func(t *testing.T) (*auditFixture, uuid.UUID)
}

// viewScenarios are the record shapes the views have to answer for. The scenarios are
// the ones the reconstruction corpus already samples, reused rather than rebuilt, so
// every view is asserted against the same real runs (constitution IX).
func viewScenarios() []viewScenario {
	return []viewScenario{
		{name: "clean_fanout", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			f := newAuditFixture(t)
			return f, f.runSucceeds(f.threePhaseWorkflow()).RunID
		}},
		{name: "retry_then_success", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := retryThenSuccess(t)
			return tc.f, tc.runID
		}},
		{name: "failure_after_retries", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := failureAfterRetries(t)
			return tc.f, tc.runID
		}},
		{name: "degraded_continue", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := degradedContinue(t)
			return tc.f, tc.runID
		}},
		{name: "paused_between_phases", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := pausedBetweenPhases(t)
			return tc.f, tc.runID
		}},
		{name: "takeover_while_paused", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := takeoverWhilePaused(t)
			return tc.f, tc.runID
		}},
		{name: "drift_override", run: func(t *testing.T) (*auditFixture, uuid.UUID) {
			tc := driftOverride(t)
			return tc.f, tc.runID
		}},
	}
}

// TestVerifyTimelineFromTheStoreAlone asserts what a reader derives from the store for
// every record shape: one entry per event in `seq` order, one entry per decision, and
// no event displaced past a decision that belongs to a later commit.
func TestVerifyTimelineFromTheStoreAlone(t *testing.T) {
	for _, sc := range viewScenarios() {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			f, runID := sc.run(t)

			timeline, err := audit.TimelineOf(ctx, f.store, runID)
			if err != nil {
				t.Fatalf("TimelineOf() error = %v", err)
			}
			if timeline.RunID != runID {
				t.Errorf("RunID = %s, want %s", timeline.RunID, runID)
			}

			events := f.events(runID)
			decisions := f.decisions(runID)
			if len(events) == 0 {
				t.Fatal("the scenario wrote no events, so the timeline cannot be exercised")
			}
			if len(timeline.Entries) != len(events)+len(decisions) {
				t.Fatalf("timeline carries %d entries, want %d events + %d decisions",
					len(timeline.Entries), len(events), len(decisions))
			}

			checkTimeline(t, timeline, events, decisions)

			// A view is computed on demand, never cached: the second read cannot
			// disagree with the first.
			again, err := audit.TimelineOf(ctx, f.store, runID)
			if err != nil {
				t.Fatalf("second TimelineOf() error = %v", err)
			}
			if len(again.Entries) != len(timeline.Entries) {
				t.Fatalf("second read has %d entries, want %d", len(again.Entries), len(timeline.Entries))
			}
			for i := range again.Entries {
				if again.Entries[i].Kind() != timeline.Entries[i].Kind() {
					t.Fatalf("entry %d is a %s on the second read, a %s on the first",
						i, again.Entries[i].Kind(), timeline.Entries[i].Kind())
				}
			}
		})
	}
}

// checkTimeline pins the two properties the interleaving must have, whichever record
// shape it is handed: events appear in `seq` order and exactly once, and every event
// after a decision carries a strictly later timestamp — a decision follows the events
// its transaction wrote, because one commit writes its events and its decisions with a
// single timestamp (contracts/store.md §1).
func checkTimeline(t *testing.T, timeline audit.Timeline, events []domain.Event, decisions []domain.Decision) {
	t.Helper()

	seqs := make([]int64, 0, len(events))
	decisionAt := make(map[uuid.UUID]int, len(decisions))
	for i, entry := range timeline.Entries {
		switch {
		case entry.Event != nil && entry.Decision != nil:
			t.Errorf("entry %d carries an event and a decision", i)
		case entry.Event == nil && entry.Decision == nil:
			t.Errorf("entry %d carries neither an event nor a decision", i)
		case entry.Decision != nil:
			if seen, ok := decisionAt[entry.Decision.ID]; ok {
				t.Errorf("decision %s appears at entries %d and %d", entry.Decision.ID, seen, i)
			}
			decisionAt[entry.Decision.ID] = i
		default:
			seqs = append(seqs, entry.Event.Seq)
		}
	}

	if len(seqs) != len(events) {
		t.Errorf("timeline carries %d events, want %d", len(seqs), len(events))
	}
	for i, event := range events {
		if i >= len(seqs) {
			t.Errorf("event seq %d is missing from the timeline", event.Seq)
			continue
		}
		if seqs[i] != event.Seq {
			t.Errorf("timeline event %d has seq %d, want %d (events are ordered by seq alone)",
				i, seqs[i], event.Seq)
		}
	}
	for _, decision := range decisions {
		if _, ok := decisionAt[decision.ID]; !ok {
			t.Errorf("decision %s (%s) is missing from the timeline", decision.ID, decision.Kind)
		}
	}

	for i, entry := range timeline.Entries {
		if entry.Decision == nil {
			continue
		}
		// Both directions of the rule, because each catches a different mistake: an
		// event of a later commit must not precede the decision, and an event of the
		// decision's own commit (or an earlier one) must not follow it.
		for j := 0; j < i; j++ {
			earlier := timeline.Entries[j].Event
			if earlier == nil {
				continue
			}
			if earlier.TS.After(entry.Decision.TS) {
				t.Errorf("event seq %d (ts %s) precedes decision %s (ts %s), but its commit wrote it later",
					earlier.Seq, earlier.TS, entry.Decision.Kind, entry.Decision.TS)
			}
		}
		for j := i + 1; j < len(timeline.Entries); j++ {
			later := timeline.Entries[j].Event
			if later == nil {
				continue
			}
			if !later.TS.After(entry.Decision.TS) {
				t.Errorf("decision %s (ts %s) precedes event seq %d (ts %s), but its commit wrote that event first",
					entry.Decision.Kind, entry.Decision.TS, later.Seq, later.TS)
			}
		}
	}
}

// TestVerifyTimelinePlacesDecisionsAtTheirCommit checks the one placement the record
// fixes exactly, for a decision whose commit wrote events before it and for a decision
// whose commit wrote no event at all.
func TestVerifyTimelinePlacesDecisionsAtTheirCommit(t *testing.T) {
	t.Run("a retry follows the failed attempt it explains", func(t *testing.T) {
		tc := retryThenSuccess(t)
		timeline := timelineOf(t, tc.f, tc.runID)

		retry := decisionEntry(t, timeline, domain.DecisionRetry)
		decision := timeline.Entries[retry].Decision

		failed := eventEntry(t, timeline, func(event domain.Event) bool {
			return event.Type == string(domain.EventAttemptFailed) && payloadAttemptNo(event) == 1
		})
		started := eventEntry(t, timeline, func(event domain.Event) bool {
			return event.Type == string(domain.EventAttemptStarted) && payloadAttemptNo(event) == 2
		})
		if !(failed < retry && retry < started) {
			t.Fatalf("retry decision at %d, want it between the failed attempt at %d and the restart at %d",
				retry, failed, started)
		}

		// Its commit wrote that event, so the two share one timestamp.
		lastEvent := timeline.Entries[retry-1].Event
		if lastEvent == nil || !lastEvent.TS.Equal(decision.TS) {
			t.Errorf("the entry before the retry decision is %+v, want the event of its own commit (ts %s)",
				timeline.Entries[retry-1], decision.TS)
		}
	})

	t.Run("a join follows the phase it decided", func(t *testing.T) {
		f := newAuditFixture(t)
		runID := f.runSucceeds(f.threePhaseWorkflow()).RunID
		timeline := timelineOf(t, f, runID)

		join := decisionEntry(t, timeline, domain.DecisionJoin)
		decision := timeline.Entries[join].Decision

		succeeded := eventEntry(t, timeline, func(event domain.Event) bool {
			return event.Type == string(domain.EventPhaseSucceeded) && event.Phase != nil && *event.Phase == "build"
		})
		if succeeded > join {
			t.Fatalf("join decision at %d, want it after the build phase.succeeded at %d", join, succeeded)
		}
		lastEvent := timeline.Entries[join-1].Event
		if lastEvent == nil || !lastEvent.TS.Equal(decision.TS) {
			t.Errorf("the entry before the join decision is %+v, want the event of its own commit (ts %s)",
				timeline.Entries[join-1], decision.TS)
		}
	})

	t.Run("a decision-only commit sits between its neighbours", func(t *testing.T) {
		tc := degradedContinue(t)
		timeline := timelineOf(t, tc.f, tc.runID)

		// `on_failure: continue` commits the policy decision and nothing else: the
		// degraded phase is already recorded, so the decision has no event of its own.
		policy := decisionEntry(t, timeline, domain.DecisionFailurePolicy)
		decision := timeline.Entries[policy].Decision

		degraded := eventEntry(t, timeline, func(event domain.Event) bool {
			return event.Type == string(domain.EventPhaseDegraded) && event.Phase != nil && *event.Phase == "flaky"
		})
		if degraded > policy {
			t.Fatalf("policy decision at %d, want it after the phase.degraded of flaky at %d", policy, degraded)
		}
		before := timeline.Entries[policy-1].Event
		if before == nil || !before.TS.Before(decision.TS) {
			t.Errorf("the entry before the policy decision is %+v, want the previous commit's event (ts %s)",
				timeline.Entries[policy-1], decision.TS)
		}
		if policy+1 >= len(timeline.Entries) {
			t.Fatalf("the policy decision is the last entry; the run continues after it")
		}
		after := timeline.Entries[policy+1].Event
		if after == nil || !after.TS.After(decision.TS) {
			t.Errorf("the entry after the policy decision is %+v, want the next commit's event (ts %s)",
				timeline.Entries[policy+1], decision.TS)
		}
	})

	t.Run("a takeover follows the run.taken_over event", func(t *testing.T) {
		tc := takeoverWhilePaused(t)
		timeline := timelineOf(t, tc.f, tc.runID)

		takeover := decisionEntry(t, timeline, domain.DecisionTakeover)
		decision := timeline.Entries[takeover].Decision

		takenOver := eventEntry(t, timeline, func(event domain.Event) bool {
			return event.Type == string(domain.EventRunTakenOver)
		})
		if takenOver > takeover {
			t.Fatalf("takeover decision at %d, want it after run.taken_over at %d", takeover, takenOver)
		}
		lastEvent := timeline.Entries[takeover-1].Event
		if lastEvent == nil || !lastEvent.TS.Equal(decision.TS) {
			t.Errorf("the entry before the takeover decision is %+v, want run.taken_over (ts %s)",
				timeline.Entries[takeover-1], decision.TS)
		}
	})
}

// TestVerifyTimelineOnHandBuiltRecords pins the placement rule itself on records a
// store cannot be asked to produce on purpose: events and a decision sharing one commit
// timestamp, a commit that wrote no event, and an event whose timestamp is older than a
// decision's — which must still leave the events in `seq` order, because `seq` is the
// only ordering authority the record has.
func TestVerifyTimelineOnHandBuiltRecords(t *testing.T) {
	runID := uuid.New()
	base := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

	t.Run("a decision follows the events of its own commit", func(t *testing.T) {
		commit := base.Add(time.Second)
		events := []domain.Event{
			{Seq: 1, RunID: runID, TS: base, Type: string(domain.EventRunStarted)},
			{Seq: 2, RunID: runID, TS: commit, Type: string(domain.EventAttemptFailed)},
		}
		decisions := []domain.Decision{
			{ID: uuid.New(), RunID: runID, TS: commit, Kind: domain.DecisionRetry},
		}

		timeline := audit.BuildTimeline(runID, events, decisions)
		wantKinds := []string{"event", "event", "decision"}
		if got := timelineKinds(timeline); !reflect.DeepEqual(got, wantKinds) {
			t.Fatalf("entry kinds = %v, want %v", got, wantKinds)
		}
		if seq := timeline.Entries[0].Event.Seq; seq != 1 {
			t.Errorf("first entry is event seq %d, want 1", seq)
		}
		if seq := timeline.Entries[1].Event.Seq; seq != 2 {
			t.Errorf("second entry is event seq %d, want 2 (the decision's commit wrote it)", seq)
		}
	})

	t.Run("a commit that wrote no event keeps its decision in place", func(t *testing.T) {
		events := []domain.Event{
			{Seq: 1, RunID: runID, TS: base, Type: string(domain.EventRunStarted)},
			{Seq: 2, RunID: runID, TS: base.Add(2 * time.Second), Type: string(domain.EventRunSucceeded)},
		}
		decisions := []domain.Decision{
			{ID: uuid.New(), RunID: runID, TS: base.Add(time.Second), Kind: domain.DecisionFailurePolicy},
		}

		timeline := audit.BuildTimeline(runID, events, decisions)
		wantKinds := []string{"event", "decision", "event"}
		if got := timelineKinds(timeline); !reflect.DeepEqual(got, wantKinds) {
			t.Fatalf("entry kinds = %v, want %v", got, wantKinds)
		}
		if seq := timeline.Entries[2].Event.Seq; seq != 2 {
			t.Errorf("last entry is event seq %d, want 2", seq)
		}
	})

	t.Run("events keep seq order when a timestamp regresses", func(t *testing.T) {
		events := []domain.Event{
			{Seq: 1, RunID: runID, TS: base.Add(time.Second), Type: string(domain.EventRunStarted)},
			{Seq: 2, RunID: runID, TS: base.Add(3 * time.Second), Type: string(domain.EventRunSucceeded)},
		}
		decisions := []domain.Decision{
			{ID: uuid.New(), RunID: runID, TS: base, Kind: domain.DecisionTakeover},
		}

		timeline := audit.BuildTimeline(runID, events, decisions)
		wantKinds := []string{"decision", "event", "event"}
		if got := timelineKinds(timeline); !reflect.DeepEqual(got, wantKinds) {
			t.Fatalf("entry kinds = %v, want %v", got, wantKinds)
		}
	})

	t.Run("an empty record yields no entries", func(t *testing.T) {
		timeline := audit.BuildTimeline(runID, nil, nil)
		if timeline.RunID != runID {
			t.Errorf("RunID = %s, want %s", timeline.RunID, runID)
		}
		if len(timeline.Entries) != 0 {
			t.Errorf("entries = %d, want none", len(timeline.Entries))
		}
	})
}

// TestVerifyStatusViewMatchesTheStoreAndTheEngine asserts the position report: the run
// row's fields, the phase rows, and the newest events come from the store exactly as
// they are, and the view agrees with Engine.Status field for field — one view, one
// vocabulary, so an operator and a reader cannot be told different things.
func TestVerifyStatusViewMatchesTheStoreAndTheEngine(t *testing.T) {
	for _, sc := range viewScenarios() {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			f, runID := sc.run(t)

			view, err := audit.Status(ctx, f.store, runID)
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}

			run, err := f.store.LoadRun(ctx, runID)
			if err != nil {
				t.Fatalf("LoadRun() error = %v", err)
			}
			phases, err := f.store.Phases(ctx, runID)
			if err != nil {
				t.Fatalf("Phases() error = %v", err)
			}
			events := f.events(runID)
			if len(events) <= audit.LatestEvents {
				t.Fatalf("the scenario wrote %d events, so the view is never exercised past its tail of %d",
					len(events), audit.LatestEvents)
			}

			if view.RunID != run.ID {
				t.Errorf("RunID = %s, want %s", view.RunID, run.ID)
			}
			if view.Status != run.Status {
				t.Errorf("Status = %q, want %q", view.Status, run.Status)
			}
			if !samePhase(view.CurrentPhase, run.CurrentPhase) {
				t.Errorf("CurrentPhase = %v, want %v", view.CurrentPhase, run.CurrentPhase)
			}
			if view.Budget != run.Budget {
				t.Errorf("Budget = %+v, want %+v", view.Budget, run.Budget)
			}
			if view.Spent != run.Spent {
				t.Errorf("Spent = %+v, want %+v", view.Spent, run.Spent)
			}
			if !samePhase(view.OwnerID, run.OwnerID) {
				t.Errorf("OwnerID = %v, want %v", view.OwnerID, run.OwnerID)
			}
			if !reflect.DeepEqual(view.Phases, phases) {
				t.Errorf("Phases = %+v, want the store's rows %+v", view.Phases, phases)
			}
			if view.NextAction != audit.NextAction(run, phases) {
				t.Errorf("NextAction = %q, want %q", view.NextAction, audit.NextAction(run, phases))
			}

			want := events[len(events)-audit.LatestEvents:]
			if !reflect.DeepEqual(view.LastEvents, want) {
				t.Errorf("LastEvents = %+v, want the last %d events %+v", view.LastEvents, audit.LatestEvents, want)
			}

			status, err := f.engine.Status(ctx, runID)
			if err != nil {
				t.Fatalf("Engine.Status() error = %v", err)
			}
			if status.RunID != view.RunID || status.Status != view.Status ||
				!samePhase(status.CurrentPhase, view.CurrentPhase) {
				t.Errorf("Engine.Status() reports %s/%s/%v, the view reports %s/%s/%v",
					status.RunID, status.Status, status.CurrentPhase, view.RunID, view.Status, view.CurrentPhase)
			}
			if status.Budget != view.Budget || status.Spent != view.Spent {
				t.Errorf("Engine.Status() budget %+v spent %+v, the view %+v/%+v",
					status.Budget, status.Spent, view.Budget, view.Spent)
			}
			if !samePhase(status.OwnerID, view.OwnerID) || !reflect.DeepEqual(status.Phases, view.Phases) {
				t.Errorf("Engine.Status() owner %v phases %+v, the view %v/%+v",
					status.OwnerID, status.Phases, view.OwnerID, view.Phases)
			}
			if status.NextAction != view.NextAction {
				t.Errorf("Engine.Status() NextAction = %q, the view = %q", status.NextAction, view.NextAction)
			}
			if !reflect.DeepEqual(status.LastEvents, view.LastEvents) {
				t.Errorf("Engine.Status() events %+v, the view %+v", status.LastEvents, view.LastEvents)
			}
		})
	}
}

// TestVerifyReportComposesChainAndFingerprints asserts the verification view: one
// report carrying both checks, the counts of what it looked at, and the verdict an
// operator acts on. Neither check is optional, so a tampered event and an altered
// artifact in one record are both reported.
func TestVerifyReportComposesChainAndFingerprints(t *testing.T) {
	ctx := context.Background()
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	now := time.Date(2026, 11, 17, 10, 30, 0, 0, time.UTC)

	events := f.events(run.RunID)
	refs, err := f.store.Artifacts(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(events) < 2 || len(refs) == 0 {
		t.Fatalf("the run recorded %d events and %d artifacts, too few to verify", len(events), len(refs))
	}

	report := audit.Verify(ctx, run.RunID, events, refs, f.artifacts, now)
	if !report.OK() {
		t.Fatalf("Verify() reported problems on a clean run: %+v", report.Problems)
	}
	if report.RunID != run.RunID {
		t.Errorf("RunID = %s, want %s", report.RunID, run.RunID)
	}
	if report.Events != len(events) {
		t.Errorf("Events = %d, want %d", report.Events, len(events))
	}
	if report.Artifacts != len(refs) {
		t.Errorf("Artifacts = %d, want %d", report.Artifacts, len(refs))
	}
	if !report.VerifiedAt.Equal(now) {
		t.Errorf("VerifiedAt = %s, want %s", report.VerifiedAt, now)
	}

	// The engine's verification is this view: same verdict, same counts.
	viaEngine, err := f.engine.Verify(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Engine.Verify() error = %v", err)
	}
	if !viaEngine.OK() {
		t.Errorf("Engine.Verify() reported problems on a clean run: %+v", viaEngine.Problems)
	}
	if viaEngine.Events != report.Events || viaEngine.Artifacts != report.Artifacts {
		t.Errorf("Engine.Verify() counted %d events/%d artifacts, the view %d/%d",
			viaEngine.Events, viaEngine.Artifacts, report.Events, report.Artifacts)
	}

	// Both checks land in one report: tamper with an event and alter the bytes behind
	// an artifact row. A reader has to learn about both, not just the first one found.
	tampered := append([]domain.Event(nil), events...)
	tampered[1].Payload = json.RawMessage(`{"tampered":true}`)
	victim := refs[0]
	if err := os.WriteFile(f.artifacts.Path(victim.SHA256), []byte("no longer the recorded bytes"), 0o640); err != nil {
		t.Fatalf("corrupt artifact %s: %v", victim.SHA256, err)
	}

	report = audit.Verify(ctx, run.RunID, tampered, refs, f.artifacts, now)
	if report.OK() {
		t.Fatal("Verify() accepted a tampered chain and an altered artifact")
	}
	chainProblem := findProblem(report.Problems, audit.ProblemChain, "")
	if chainProblem == nil {
		t.Errorf("no chain problem reported: %+v", report.Problems)
	} else if chainProblem.Seq != tampered[1].Seq {
		t.Errorf("chain problem names seq %d, want %d", chainProblem.Seq, tampered[1].Seq)
	}
	artifactProblem := findProblem(report.Problems, audit.ProblemDigestMismatch, victim.SHA256)
	if artifactProblem == nil {
		t.Fatalf("no digest mismatch reported for %s: %+v", victim.SHA256, report.Problems)
	}
	if !strings.Contains(artifactProblem.Detail, victim.RelativePath) {
		t.Errorf("problem detail %q does not name the file %s", artifactProblem.Detail, victim.RelativePath)
	}
	if report.Events != len(tampered) || report.Artifacts != len(refs) {
		t.Errorf("the failing report counted %d events/%d artifacts, want %d/%d",
			report.Events, report.Artifacts, len(tampered), len(refs))
	}
}

// timelineOf derives a timeline and fails the test on an unexpected error.
func timelineOf(t *testing.T, f *auditFixture, runID uuid.UUID) audit.Timeline {
	t.Helper()

	timeline, err := audit.TimelineOf(context.Background(), f.store, runID)
	if err != nil {
		t.Fatalf("TimelineOf() error = %v", err)
	}
	return timeline
}

// timelineKinds lists the kind of every entry, for comparing an order outright.
func timelineKinds(timeline audit.Timeline) []string {
	kinds := make([]string, 0, len(timeline.Entries))
	for _, entry := range timeline.Entries {
		kinds = append(kinds, entry.Kind())
	}
	return kinds
}

// decisionEntry is the index of the only entry holding a decision of one kind.
func decisionEntry(t *testing.T, timeline audit.Timeline, kind domain.DecisionKind) int {
	t.Helper()

	found := -1
	for i, entry := range timeline.Entries {
		if entry.Decision == nil || entry.Decision.Kind != kind {
			continue
		}
		if found >= 0 {
			t.Fatalf("the timeline carries more than one %s decision (entries %d and %d)", kind, found, i)
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("decisionEntry() found no %s decision in %d entries", kind, len(timeline.Entries))
	}
	return found
}

// eventEntry is the index of the only entry holding an event the predicate matches.
func eventEntry(t *testing.T, timeline audit.Timeline, match func(domain.Event) bool) int {
	t.Helper()

	found := -1
	for i, entry := range timeline.Entries {
		if entry.Event == nil || !match(*entry.Event) {
			continue
		}
		if found >= 0 {
			t.Fatalf("the timeline carries more than one matching event (entries %d and %d)", found, i)
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("eventEntry() matched no event in %d entries", len(timeline.Entries))
	}
	return found
}

// payloadAttemptNo reads the attempt number an attempt event carries.
func payloadAttemptNo(event domain.Event) int {
	var payload struct {
		AttemptNo int `json:"attempt_no"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return 0
	}
	return payload.AttemptNo
}
