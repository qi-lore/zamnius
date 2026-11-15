package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/pkg/zamnius"
)

// runResume executes `zamnius run resume`. Resuming is the operator's recovery
// path, so the command refuses nothing it can explain: a run owned elsewhere names
// the live owner and its expiry, a terminal run is a no-op, and a supplied
// definition that has drifted is refused unless the operator overrides it
// (contracts/cli.md §1).
func (r *runner) runResume(args []string) int {
	const command = "run.resume"

	positional, flags := splitArgs(args)
	if len(positional) != 1 {
		return r.usage("run resume needs exactly one run id")
	}

	var shared runFlags
	var file string
	var allowDrift bool
	set := r.newFlagSet(command)
	addCommon(set, &shared)
	set.StringVar(&file, "file", "", "definition file the run was started from (optional)")
	set.BoolVar(&allowDrift, "allow-definition-drift", false,
		"permit resuming when the supplied definition fingerprint has drifted")
	if ok, code := r.parseFlags(set, flags); !ok {
		return code
	}
	r.json = shared.json

	runID, parseErr := uuid.Parse(positional[0])
	if parseErr != nil {
		return r.usage(fmt.Sprintf("%q is not a run id", positional[0]))
	}

	composition, closeFn, err := r.openSettings(shared)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	defer closeFn()

	// A terminal run is immutable, so the answer is read with the inspection engine:
	// resuming it needs no model credential and must execute nothing (FR-018).
	inspector, err := composition.Inspector()
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	status, err := inspector.Status(r.ctx, runID)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	if status.Status.Terminal() {
		refs, err := inspector.Artifacts(r.ctx, runID)
		if err != nil {
			return r.finish(command, nil, nil, "", err, nil)
		}
		view := viewOf(status, refs)
		return r.finish(command, view, artifactViews(refs), status.Status, nil, func() { r.alreadyComplete(view) })
	}

	// The supplied definition is an intent check, never a swap: the engine finishes
	// the recorded workflow either way (data-model.md §10).
	supplied, err := loadSuppliedDefinition(r.ctx, file)
	if err != nil {
		return r.finish(command, nil, nil, status.Status, err, nil)
	}

	// Resuming executes work, so it needs the model credential. A run that is
	// terminal or held by another process is refused before any model call.
	engine, err := composition.Engine()
	if err != nil {
		return r.finish(command, nil, nil, status.Status, err, nil)
	}

	result, resumeErr := engine.Resume(r.ctx, runID, zamnius.ResumeOptions{
		Definition:           supplied,
		AllowDefinitionDrift: allowDrift,
		MaxConcurrency:       composition.Settings.MaxConcurrency,
	})
	if errors.Is(resumeErr, domain.ErrRunAlreadyOwned) {
		// Re-read the position so the owner named is the one that actually refused
		// the lease, not whatever the pre-check saw a moment earlier.
		live := status
		if refreshed, refreshErr := engine.Status(r.ctx, runID); refreshErr == nil {
			live = refreshed
		}
		resumeErr = ownerConflict(resumeErr, live)
	}
	return r.reportOutcome(command, engine, result, resumeErr)
}

// loadSuppliedDefinition parses the optional --file value. An empty value means the
// run resumes from its stored snapshot alone, so no definition is compared.
func loadSuppliedDefinition(ctx context.Context, file string) (*definition.Definition, error) {
	if strings.TrimSpace(file) == "" {
		return nil, nil
	}
	def, err := definition.LoadFile(ctx, file)
	if err != nil {
		return nil, err
	}
	return &def, nil
}

// ownerConflict turns the store's terse lease error into the diagnostic the contract
// requires: it names the live owner and when its claim lapses, which is what lets an
// operator decide between waiting and stopping the owning process (contracts/cli.md
// §1, FR-016).
func ownerConflict(err error, status zamnius.Status) error {
	hint := fmt.Sprintf("run %s is held by another process", status.RunID)
	if status.OwnerID != nil && *status.OwnerID != "" {
		hint = fmt.Sprintf("run %s is owned by %s", status.RunID, *status.OwnerID)
		if status.LeaseExpiresAt != nil {
			hint += fmt.Sprintf(" until %s", domain.FormatTimestamp(*status.LeaseExpiresAt))
		}
	}
	hint += "; resume it after that lease expires, or stop the owning process"

	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   err.Error(),
		Hint:  hint,
		Err:   err,
	}
}
