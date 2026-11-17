package cli

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/pkg/zamnius"
)

// runStatus executes `zamnius run status`: the position report for one run. The
// inspection engine takes no lease, so a reader can never interfere with an owner,
// and the credential that produced the run is not needed to read it
// (contracts/cli.md §1).
func (r *runner) runStatus(args []string) int {
	return r.inspectCommand("run.status", args, func(engine *zamnius.Engine, runID uuid.UUID) int {
		status, err := engine.Status(r.ctx, runID)
		if err != nil {
			return r.finish("run.status", nil, nil, "", err, nil)
		}
		refs, err := engine.Artifacts(r.ctx, runID)
		if err != nil {
			return r.finish("run.status", nil, nil, "", err, nil)
		}
		view := viewOf(status, refs)
		return r.finish("run.status", view, artifactViews(refs), status.Status, nil, func() { r.humanStatus(view) })
	})
}

// humanStatus prints the prose form of a status view: the position, the per-phase
// rows, the consumed budget, and the last five events. Those are the fields the
// contract names, and they are the same data the `--json` form carries, so the two
// renderings cannot drift (contracts/cli.md §1).
func (r *runner) humanStatus(view *runView) {
	writeLine(r.stdout, "run %s", view.ID)
	writeLine(r.stdout, "status: %s", view.Status)
	if view.CurrentPhase != nil {
		writeLine(r.stdout, "current phase: %s", *view.CurrentPhase)
	}
	for _, phase := range view.Phases {
		line := fmt.Sprintf("  %-20s %-10s %s", phase.Name, phase.Status, plural(phase.Attempts, "attempt"))
		if phase.Degraded {
			line += " (degraded)"
		}
		if phase.FailureReason != nil {
			line += ": " + *phase.FailureReason
		}
		writeLine(r.stdout, "%s", line)
	}
	writeLine(r.stdout, "spent: %s, %d tokens in, %d tokens out",
		plural(view.Spent.Attempts, "attempt"), view.Spent.TokensIn, view.Spent.TokensOut)
	if view.NextAction != "" {
		writeLine(r.stdout, "next: %s", view.NextAction)
	}
	if len(view.LastEvents) > 0 {
		writeLine(r.stdout, "last events:")
		for _, event := range view.LastEvents {
			writeLine(r.stdout, "  %6d %-24s %s%s", event.Seq, event.Type, event.Actor, phaseSuffix(event.Phase))
		}
	}
}

// phaseSuffix renders an event's phase as a trailing "(phase)" when it has one, so
// a run-level event is not padded with an empty column.
func phaseSuffix(phase string) string {
	if phase == "" {
		return ""
	}
	return " (" + phase + ")"
}
