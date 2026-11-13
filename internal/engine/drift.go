package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// definitionDrift is how a resume's definition disagrees with the run's recorded
// fingerprint. The snapshot is always compared, so a record that does not describe
// itself is caught; a caller-supplied workflow is compared too, because the
// operator's file is what drift actually happens to (data-model.md §10, FR-014).
type definitionDrift struct {
	expected string
	snapshot string
	// supplied is empty when the caller supplied no definition: resuming from the
	// persisted store alone must stay possible (FR-012).
	supplied string
}

// drifted reports whether anything disagrees with the run's recorded fingerprint.
func (d definitionDrift) drifted() bool {
	return d.snapshot != d.expected || (d.supplied != "" && d.supplied != d.expected)
}

// compareDefinitions computes the fingerprints a resume has to weigh against the
// run's record. A nil supplied definition means the caller is resuming from the
// persisted snapshot alone.
func compareDefinitions(run domain.Run, snapshot definition.Definition, supplied *definition.Definition) (definitionDrift, error) {
	snapshotFingerprint, err := definition.Fingerprint(snapshot)
	if err != nil {
		return definitionDrift{}, err
	}
	drift := definitionDrift{expected: run.DefinitionFingerprint, snapshot: snapshotFingerprint}
	if supplied == nil {
		return drift, nil
	}
	if drift.supplied, err = definition.Fingerprint(*supplied); err != nil {
		return definitionDrift{}, err
	}
	return drift, nil
}

// refuseDefinitionDrift is the refusal an operator can act on: it names the
// fingerprint the run was created from, whichever fingerprints disagree with it,
// and the flag that proceeds anyway.
func refuseDefinitionDrift(run domain.Run, drift definitionDrift) error {
	var detail strings.Builder
	fmt.Fprintf(&detail, "run %s was created from fingerprint %s", run.ID, drift.expected)
	if drift.snapshot != drift.expected {
		fmt.Fprintf(&detail, "; its stored snapshot hashes to %s", drift.snapshot)
	}
	if drift.supplied != "" && drift.supplied != drift.expected {
		fmt.Fprintf(&detail, "; the supplied definition hashes to %s", drift.supplied)
	}
	detail.WriteString("; pass --allow-definition-drift to proceed and record the override")

	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "definition does not match the run",
		Hint:  detail.String(),
		Err:   domain.ErrDefinitionDrift,
	}
}

// recordDefinitionDriftOverride writes the deliberate override as a decision, so a
// reader can tell why a run executed a workflow its record did not expect. It runs
// only after the lease is held: a process that does not own the run must not append
// to its history.
//
// The engine still executes the stored snapshot. Committed phases were recorded
// against it, so supplying a different definition is an intent check, not a swap:
// the run finishes the workflow it started (data-model.md §10).
func (e *Engine) recordDefinitionDriftOverride(ctx context.Context, run domain.Run, drift definitionDrift) error {
	evidence := map[string]any{
		"run_id":               run.ID.String(),
		"expected_fingerprint": drift.expected,
		"snapshot_fingerprint": drift.snapshot,
		"definition_name":      run.DefinitionName,
	}
	if drift.supplied != "" {
		evidence["supplied_fingerprint"] = drift.supplied
	}

	rationale := fmt.Sprintf("the operator allowed definition drift: expected %s, stored snapshot %s",
		drift.expected, drift.snapshot)
	if drift.supplied != "" {
		rationale += fmt.Sprintf(", supplied %s", drift.supplied)
	}

	if err := e.store.CommitRunResult(ctx, domain.RunCommit{
		RunID: run.ID,
		Events: []domain.EventDraft{{
			Type:    domain.EventDecisionRecorded,
			Actor:   domain.ActorOperator(e.owner),
			Payload: map[string]any{"kind": string(domain.DecisionResumeRestart), "reason": "definition drift override"},
		}},
		Decisions: []domain.DecisionDraft{{
			Kind:      domain.DecisionResumeRestart,
			Summary:   "definition drift accepted by explicit override",
			Rationale: rationale,
			Evidence:  evidence,
		}},
	}); err != nil {
		return err
	}
	e.logger.Warn("definition drift override recorded",
		"run_id", run.ID.String(), "expected", drift.expected,
		"snapshot", drift.snapshot, "supplied", drift.supplied)
	return nil
}
