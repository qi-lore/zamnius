package audit

// The reconstruction view: what a reader who was not present during a run can state
// from persisted records alone (FR-023, SC-004, data-model.md §8). Like every view in
// this package it is computed on demand and never stored — a cached projection is
// exactly what an auditor cannot trust (Principle III).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// Reader is the slice of the store the reconstruction needs (contracts/store.md §2).
// It is a port rather than the concrete store so the view works over any durable
// reader, and so a test can hand it nothing else.
type Reader interface {
	LoadRun(ctx context.Context, id uuid.UUID) (domain.Run, error)
	Phases(ctx context.Context, runID uuid.UUID) ([]domain.PhaseState, error)
	Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]domain.Event, error)
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
		NextAction:   nextAction(run, phases),
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

// nextAction states the next expected action. The wording is the one Engine.Status
// publishes: two readers of one record telling an operator different next steps would
// make both untrustworthy, and TestVerifyReconstructionFromTheStoreAlone pins the two
// together.
func nextAction(run domain.Run, phases []domain.PhaseState) string {
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
