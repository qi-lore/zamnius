package audit

// The audit views: what a reader who was not present during a run can state from
// persisted records alone — the ordered timeline, the position and the work still owed
// (FR-023, SC-004, data-model.md §8), the run status report, and the verification view.
// Like every view in this package each is computed on demand and never stored — a
// cached projection is exactly what an auditor cannot trust (Principle III).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// Reader is the slice of the store the position and reconstruction views need
// (contracts/store.md §2). It is a port rather than the concrete store so a view works
// over any durable reader, and so a test can hand it nothing else.
type Reader interface {
	LoadRun(ctx context.Context, id uuid.UUID) (domain.Run, error)
	Phases(ctx context.Context, runID uuid.UUID) ([]domain.PhaseState, error)
	Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]domain.Event, error)
}

// TimelineReader is the read surface the timeline needs: the event log it orders and
// the decision records it interleaves (contracts/store.md §2).
type TimelineReader interface {
	Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]domain.Event, error)
	Decisions(ctx context.Context, runID uuid.UUID) ([]domain.Decision, error)
}

// TimelineEntry is one member of a run's ordered history: an event, or a decision
// record placed where the transaction that wrote it put it. Exactly one of the two
// pointers is set, so a reader can tell the two facts apart instead of guessing from
// empty fields.
type TimelineEntry struct {
	Event    *domain.Event    `json:"event,omitempty"`
	Decision *domain.Decision `json:"decision,omitempty"`
}

// Kind names the record the entry carries.
func (e TimelineEntry) Kind() string {
	switch {
	case e.Decision != nil:
		return "decision"
	case e.Event != nil:
		return "event"
	default:
		return ""
	}
}

// Timeline is a run's history in order, as a reader who was not present sees it.
type Timeline struct {
	RunID   uuid.UUID       `json:"run_id"`
	Entries []TimelineEntry `json:"entries"`
}

// TimelineOf reads a run's record and derives its timeline from it.
func TimelineOf(ctx context.Context, reader TimelineReader, runID uuid.UUID) (Timeline, error) {
	events, err := reader.Events(ctx, runID, 0)
	if err != nil {
		return Timeline{}, fmt.Errorf("timeline of run %s: %w", runID, err)
	}
	decisions, err := reader.Decisions(ctx, runID)
	if err != nil {
		return Timeline{}, fmt.Errorf("timeline of run %s: %w", runID, err)
	}
	return BuildTimeline(runID, events, decisions), nil
}

// BuildTimeline interleaves a run's events and decision records into one ordered
// history.
//
// Events appear in `seq` order, always: `seq` is the only ordering authority the
// record has, and a view that reordered them would be inventing a history
// (data-model.md §6). A decision record carries no sequence number of its own
// (data-model.md §7), so the timeline can place it only where the record fixes it:
// after the events its transaction wrote. One transaction writes its events and its
// decisions with a single timestamp, events first (contracts/store.md §1), so a
// decision follows the events sharing its timestamp and precedes the next
// transaction's. A decision committed in a transaction that wrote no event — a
// `continue` degradation is one — lands between its neighbours, where it belongs.
func BuildTimeline(runID uuid.UUID, events []domain.Event, decisions []domain.Decision) Timeline {
	entries := make([]TimelineEntry, 0, len(events)+len(decisions))
	next := 0
	for i := range decisions {
		decision := decisions[i]
		for next < len(events) && !events[next].TS.After(decision.TS) {
			event := events[next]
			entries = append(entries, TimelineEntry{Event: &event})
			next++
		}
		entries = append(entries, TimelineEntry{Decision: &decision})
	}
	for ; next < len(events); next++ {
		event := events[next]
		entries = append(entries, TimelineEntry{Event: &event})
	}
	return Timeline{RunID: runID, Entries: entries}
}

// LatestEvents is how many of the newest events the run status view carries: what
// `zamnius run status` prints and returns in its `--json` form (contracts/cli.md §1).
const LatestEvents = 5

// StatusView is the position report a reader sees: the run row's fields, its phase
// rows, the newest events, and the next expected action. The row's fields are carried
// rather than the row itself, so a view never re-serializes the stored definition
// snapshot; nothing here is stored twice, and none of it takes the lease, so a run
// another process owns is still inspectable (FR-023).
type StatusView struct {
	RunID        uuid.UUID           `json:"run_id"`
	Status       domain.RunStatus    `json:"status"`
	CurrentPhase *string             `json:"current_phase,omitempty"`
	Phases       []domain.PhaseState `json:"phases"`
	Budget       domain.Budget       `json:"budget"`
	Spent        domain.Consumption  `json:"spent"`
	// OwnerID and LeaseExpiresAt are what explain a refused resume: they name the
	// process holding the run and when its claim lapses (contracts/cli.md §1).
	OwnerID        *string        `json:"owner_id,omitempty"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
	NextAction     string         `json:"next_action"`
	LastEvents     []domain.Event `json:"last_events,omitempty"`
}

// Status derives the run status view from the store.
func Status(ctx context.Context, reader Reader, runID uuid.UUID) (StatusView, error) {
	run, err := reader.LoadRun(ctx, runID)
	if err != nil {
		return StatusView{}, fmt.Errorf("status of run %s: %w", runID, err)
	}
	phases, err := reader.Phases(ctx, runID)
	if err != nil {
		return StatusView{}, fmt.Errorf("status of run %s: %w", runID, err)
	}
	events, err := reader.Events(ctx, runID, 0)
	if err != nil {
		return StatusView{}, fmt.Errorf("status of run %s: %w", runID, err)
	}
	if len(events) > LatestEvents {
		events = events[len(events)-LatestEvents:]
	}

	return StatusView{
		RunID:          run.ID,
		Status:         run.Status,
		CurrentPhase:   run.CurrentPhase,
		Phases:         phases,
		Budget:         run.Budget,
		Spent:          run.Spent,
		OwnerID:        run.OwnerID,
		LeaseExpiresAt: run.LeaseExpiresAt,
		NextAction:     NextAction(run, phases),
		LastEvents:     events,
	}, nil
}

// Verify derives the verification view from an already-loaded record: every event's
// hash and link, and every artifact row checked against the file it names. It takes the
// loaded facts rather than a reader because the artifact row index is an optional read
// the state contract does not put on the store port (contracts/store.md §4), so the
// caller owns its own read paths and the view stays a pure projection of them.
//
// The chain is recomputed by streaming the log through a ChainVerifier, one event at a
// time (research.md R12), and the count comes from that same pass, so a run's verdict
// and its event count cannot disagree.
//
// A report is returned even when a check fails, because the problems and the counts
// are together the answer: a chain failure, a missing file, and a digest mismatch are
// distinguished for the operator (contracts/cli.md §3).
func Verify(ctx context.Context, runID uuid.UUID, events []domain.Event, refs []domain.ArtifactRef, verifier ArtifactVerifier, now time.Time) Report {
	chain := NewChainVerifier()
	for i := range events {
		chain.Add(events[i])
	}
	problems := append(chain.Problems(), VerifyArtifacts(ctx, refs, verifier)...)

	return Report{
		RunID:      runID,
		Events:     chain.Count(),
		Artifacts:  len(refs),
		VerifiedAt: now,
		Problems:   problems,
	}
}

// Work is one declared unit of work and the status the record gives it: a phase
// (Branch empty), or one branch of a fan-out phase. A completed phase carries its
// PhaseStatus; a completed branch carries its AttemptStatus; unfinished work carries
// `pending` or the phase's non-terminal status.
type Work struct {
	Phase  string `json:"phase"`
	Branch string `json:"branch,omitempty"`
	Status string `json:"status"`
}

// Reconstruction is the reader's answer: where the run stopped, what it completed,
// what it still owes, and what the next expected action is.
type Reconstruction struct {
	RunID       uuid.UUID        `json:"run_id"`
	Definition  string           `json:"definition"`
	Fingerprint string           `json:"definition_fingerprint"`
	Status      domain.RunStatus `json:"status"`
	// CurrentPhase is the position the run row records: the phase a resume continues
	// at, or the phase a terminal run ended on.
	CurrentPhase *string `json:"current_phase,omitempty"`
	// Reason is the run's recorded outcome reason: why it paused, failed, or ended.
	Reason *string `json:"reason,omitempty"`
	// Completed and Pending partition the declared work: a phase or branch is
	// completed once the record gives it an outcome, and pending while it has none.
	Completed  []Work `json:"completed"`
	Pending    []Work `json:"pending"`
	NextAction string `json:"next_action"`
}

// Reconstruct derives the view from the store. It is deliberately total: a run that
// paused, failed, or was interrupted half-way still yields a complete answer, because
// those are the states a reader most needs explained.
func Reconstruct(ctx context.Context, reader Reader, runID uuid.UUID) (Reconstruction, error) {
	run, err := reader.LoadRun(ctx, runID)
	if err != nil {
		return Reconstruction{}, fmt.Errorf("reconstruct run %s: %w", runID, err)
	}
	phases, err := reader.Phases(ctx, runID)
	if err != nil {
		return Reconstruction{}, fmt.Errorf("reconstruct run %s: %w", runID, err)
	}

	// The snapshot is what tells a reader which branches the fan-out declared, so the
	// view never infers pending work from what happens to be in the log.
	def, err := definition.FromSnapshot(run.DefinitionJSON)
	if err != nil {
		return Reconstruction{}, fmt.Errorf("reconstruct run %s: %w", runID, err)
	}

	declared := make(map[string]definition.Phase, len(def.Phases))
	for _, phase := range def.Phases {
		declared[phase.Name] = phase
	}
	states := make(map[string]domain.PhaseState, len(phases))
	for _, state := range phases {
		if _, ok := declared[state.Name]; !ok {
			// A phase row the snapshot does not declare is a record contradicting
			// itself. Guessing which side is right would hide an integrity failure.
			return Reconstruction{}, &domain.Error{
				Class: domain.ErrClassInternal,
				Msg:   fmt.Sprintf("run %s records phase %q, which its definition does not declare", runID, state.Name),
				Hint:  "the phase rows and the stored definition snapshot disagree",
				Err:   domain.ErrIntegrity,
			}
		}
		states[state.Name] = state
	}

	// Branch progress is only readable from attempt events, and only while a fan-out
	// is still running; a terminal fan-out carries its outcomes in the join result.
	// Reading the log lazily keeps the common case one query cheaper.
	needsEvents := false
	for _, phase := range def.Phases {
		state, ok := states[phase.Name]
		if ok && !state.Status.Terminal() && phase.Mode == definition.ModeFanout {
			needsEvents = true
			break
		}
	}
	var events []domain.Event
	if needsEvents {
		if events, err = reader.Events(ctx, runID, 0); err != nil {
			return Reconstruction{}, fmt.Errorf("reconstruct run %s: %w", runID, err)
		}
	}

	view := Reconstruction{
		RunID:        run.ID,
		Definition:   run.DefinitionName,
		Fingerprint:  run.DefinitionFingerprint,
		Status:       run.Status,
		CurrentPhase: run.CurrentPhase,
		Reason:       run.OutcomeReason,
		NextAction:   NextAction(run, phases),
	}

	// Declared order is the report order: a reader reconstructs the plan, not the
	// order in which the scheduler happened to finish.
	for _, phase := range def.Phases {
		state, ok := states[phase.Name]
		if !ok {
			// A declared phase without a row was never instantiated. It is pending,
			// and the honest status is the one the lifecycle starts from.
			view.Pending = append(view.Pending, Work{Phase: phase.Name, Status: string(domain.PhasePending)})
			continue
		}

		if !state.Status.Terminal() {
			view.Pending = append(view.Pending, Work{Phase: state.Name, Status: string(state.Status)})
			if phase.Mode == definition.ModeFanout {
				completed, pending := branchProgress(state.Name, phase.Branches, events)
				view.Completed = append(view.Completed, completed...)
				view.Pending = append(view.Pending, pending...)
			}
			continue
		}

		view.Completed = append(view.Completed, Work{Phase: state.Name, Status: string(state.Status)})
		if phase.Mode != definition.ModeFanout || state.JoinResult == nil {
			// A skipped fan-out never ran a branch, so it has no branch work to report.
			continue
		}
		for _, outcome := range state.JoinResult.Branches {
			view.Completed = append(view.Completed, Work{
				Phase:  state.Name,
				Branch: outcome.Branch,
				Status: string(outcome.Status),
			})
		}
	}

	return view, nil
}

// branchProgress splits the declared branches of a running fan-out into the ones whose
// last attempt event finished and the ones that still owe a result. A branch whose
// attempt was abandoned or is still in flight is pending: resume restarts it, so it is
// work the run has not done yet, not work it failed.
func branchProgress(phase string, branches []definition.Branch, events []domain.Event) (completed, pending []Work) {
	last := make(map[string]string, len(branches))
	for _, event := range events {
		if event.Phase == nil || *event.Phase != phase {
			continue
		}
		switch event.Type {
		case string(domain.EventAttemptStarted), string(domain.EventAttemptSucceeded),
			string(domain.EventAttemptFailed), string(domain.EventAttemptAbandoned):
		default:
			continue
		}
		branch := payloadBranch(event)
		if branch == "" {
			continue
		}
		switch event.Type {
		case string(domain.EventAttemptSucceeded):
			last[branch] = string(domain.AttemptSucceeded)
		case string(domain.EventAttemptFailed):
			last[branch] = string(domain.AttemptFailed)
		default:
			// A start or an abandonment leaves the branch without an outcome; a later
			// start overwrites a failed attempt, which is what a retry looks like.
			last[branch] = string(domain.PhasePending)
		}
	}

	for _, branch := range branches {
		if status, ok := last[branch.Name]; ok && status != string(domain.PhasePending) {
			completed = append(completed, Work{Phase: phase, Branch: branch.Name, Status: status})
			continue
		}
		pending = append(pending, Work{Phase: phase, Branch: branch.Name, Status: string(domain.PhasePending)})
	}
	return completed, pending
}

// payloadBranch reads the branch label an attempt event carries. The label is part of
// the event's recorded facts, which is what lets a reader attribute branch work
// without guessing from the agent name.
func payloadBranch(event domain.Event) string {
	var payload struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	return payload.Branch
}

// NextAction states the next expected action for a run in the state the record gives
// it. It is exported because the view and the engine publish one vocabulary: two
// readers of one record telling an operator different next steps would make both
// untrustworthy, and TestVerifyReconstructionFromTheStoreAlone and
// TestVerifyStatusViewMatchesTheStoreAndTheEngine pin the readers together.
func NextAction(run domain.Run, phases []domain.PhaseState) string {
	switch run.Status {
	case domain.RunSucceeded:
		return "nothing; the run is complete"
	case domain.RunFailed:
		return "inspect the failed phase, then resume after fixing its cause"
	case domain.RunCancelled:
		return "nothing; the run was cancelled"
	}
	for _, phase := range phases {
		if phase.Status == domain.PhaseRunning {
			return "resume the run; phase " + phase.Name + " was interrupted"
		}
	}
	for _, phase := range phases {
		if !phase.Status.Terminal() {
			return "resume the run to execute phase " + phase.Name
		}
	}
	return "nothing; every phase reached a terminal state"
}
