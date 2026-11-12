package engine

import (
	"context"
	"fmt"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// checkDefinitionDrift compares the fingerprint of the stored snapshot with the
// one the caller is about to execute. A mismatch is refused unless the operator
// explicitly overrides it, and the override is recorded as a decision so the run's
// history explains why a different definition produced its results (data-model.md
// §10, FR-014).
func (e *Engine) checkDefinitionDrift(ctx context.Context, run domain.Run, def definition.Definition, allowDrift bool) error {
	fingerprint, err := definition.Fingerprint(def)
	if err != nil {
		return err
	}
	if fingerprint == run.DefinitionFingerprint {
		return nil
	}

	if !allowDrift {
		return &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "definition does not match the run",
			Hint: fmt.Sprintf("run %s was created from fingerprint %s but the stored snapshot hashes to %s; pass --allow-definition-drift to proceed and record the override",
				run.ID, run.DefinitionFingerprint, fingerprint),
			Err: domain.ErrDefinitionDrift,
		}
	}

	evidence := map[string]any{
		"run_id":               run.ID.String(),
		"expected_fingerprint": run.DefinitionFingerprint,
		"supplied_fingerprint": fingerprint,
		"definition_name":      run.DefinitionName,
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
			Rationale: fmt.Sprintf("the operator allowed definition drift: expected %s, supplied %s", run.DefinitionFingerprint, fingerprint),
			Evidence:  evidence,
		}},
	}); err != nil {
		return err
	}
	e.logger.Warn("definition drift override recorded",
		"run_id", run.ID.String(), "expected", run.DefinitionFingerprint, "supplied", fingerprint)
	return nil
}
