package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/pkg/zamnius"
)

// envelope is the single object every command emits under --json. It is versioned
// separately from the binary so a script can pin the shape it parses
// (contracts/cli.md §2).
type envelope struct {
	ApiVersion string         `json:"api_version"`
	Command    string         `json:"command"`
	OK         bool           `json:"ok"`
	Run        *runView       `json:"run"`
	Artifacts  []artifactView `json:"artifacts"`
	Error      *errorView     `json:"error"`
}

// runView is the run section: the same shape for every command, so a consumer
// parses one object rather than one per subcommand.
type runView struct {
	ID           string             `json:"id"`
	Status       string             `json:"status"`
	CurrentPhase *string            `json:"current_phase,omitempty"`
	Phases       []phaseView        `json:"phases"`
	Attempts     int                `json:"attempts"`
	Budget       domain.Budget      `json:"budget"`
	Spent        domain.Consumption `json:"spent"`
	// OwnerID and LeaseExpiresAt explain a refused resume: they name the holding
	// process and when its claim lapses (contracts/cli.md §1).
	OwnerID        *string     `json:"owner_id,omitempty"`
	LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
	NextAction     string      `json:"next_action"`
	LastEvents     []eventView `json:"last_events,omitempty"`
}

// phaseView is one phase row, flattened for reading.
type phaseView struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	Attempts      int     `json:"attempts"`
	Artifacts     int     `json:"artifacts"`
	Degraded      bool    `json:"degraded,omitempty"`
	ErrorClass    string  `json:"error_class,omitempty"`
	FailureReason *string `json:"failure_reason,omitempty"`
}

// artifactView is one committed artifact, with the provenance an auditor needs to
// find the file and the phase that produced it.
type artifactView struct {
	SHA256    string `json:"sha256"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	SizeBytes int64  `json:"size_bytes"`
	Phase     string `json:"phase"`
	Expected  string `json:"digest"` // retained for the export path (T061)
}

// eventView is one entry of the ordered log.
type eventView struct {
	Seq   int64  `json:"seq"`
	Type  string `json:"type"`
	TS    string `json:"ts"`
	Actor string `json:"actor"`
	Phase string `json:"phase,omitempty"`
}

// errorView is the failure section. Hint is required for configuration and
// integrity failures and must name the setting or file to fix (contracts/cli.md §2).
type errorView struct {
	Class   string `json:"class"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// viewOf projects an engine status report and the run's artifacts into the
// envelope's run section. One projection for every command is what keeps
// `run.start` and `run.status` output interchangeable.
func viewOf(status zamnius.Status, refs []zamnius.ArtifactRef) *runView {
	perPhase := make(map[string]int, len(status.Phases))
	for _, ref := range refs {
		perPhase[ref.Phase]++
	}

	view := &runView{
		ID:             status.RunID.String(),
		Status:         string(status.Status),
		CurrentPhase:   status.CurrentPhase,
		Phases:         make([]phaseView, 0, len(status.Phases)),
		Budget:         status.Budget,
		Spent:          status.Spent,
		OwnerID:        status.OwnerID,
		LeaseExpiresAt: status.LeaseExpiresAt,
		NextAction:     status.NextAction,
	}
	for _, phase := range status.Phases {
		view.Attempts += phase.AttemptCount
		view.Phases = append(view.Phases, phaseView{
			Name:          phase.Name,
			Status:        string(phase.Status),
			Attempts:      phase.AttemptCount,
			Artifacts:     perPhase[phase.Name],
			Degraded:      phase.Degraded,
			FailureReason: phase.FailureReason,
		})
	}
	for _, event := range status.LastEvents {
		view.LastEvents = append(view.LastEvents, eventView{
			Seq:   event.Seq,
			Type:  event.Type,
			TS:    domain.FormatTimestamp(event.TS),
			Actor: event.Actor,
			Phase: deref(event.Phase),
		})
	}
	return view
}

// artifactViews projects artifact rows into the envelope's array.
func artifactViews(refs []zamnius.ArtifactRef) []artifactView {
	views := make([]artifactView, 0, len(refs))
	for _, ref := range refs {
		views = append(views, artifactView{
			SHA256:    ref.SHA256,
			Kind:      ref.Kind,
			MediaType: ref.MediaType,
			SizeBytes: ref.SizeBytes,
			Phase:     ref.Phase,
			Expected:  ref.SHA256,
		})
	}
	return views
}

// finish renders one command outcome and returns the process exit code. A failure
// writes the envelope to stdout (so a script sees the classified failure) and, in
// human mode, an actionable line with the hint to stderr.
func (r *runner) finish(command string, view *runView, artifacts []artifactView, runStatus domain.RunStatus, err error, human func()) int {
	code := exitCodeFor(err, runStatus)

	if r.json {
		out := envelope{
			ApiVersion: ApiVersion,
			Command:    command,
			OK:         err == nil,
			Run:        view,
			Artifacts:  artifacts,
		}
		if err != nil {
			class := string(domain.ClassOf(err))
			if class == "" {
				class = "usage"
			}
			out.Error = &errorView{Class: class, Message: err.Error(), Hint: domain.HintOf(err)}
		}
		r.writeJSON(out)
		return code
	}

	if err == nil {
		if human != nil {
			human()
		}
		return ExitOK
	}
	fmt.Fprintf(r.stderr, "zamnius: %s\n", err.Error())
	if hint := domain.HintOf(err); hint != "" {
		fmt.Fprintf(r.stderr, "hint: %s\n", hint)
	}
	return code
}

// writeJSON emits exactly one object on stdout. A failure to encode is reported on
// stderr but cannot change the exit code: the command's outcome is already decided.
func (r *runner) writeJSON(value envelope) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(r.stderr, "zamnius: cannot encode the JSON envelope: %v\n", err)
		return
	}
	fmt.Fprintln(r.stdout, string(encoded))
}

// summaryLine renders `run <id> finished: <status> (<n> phases, <n> attempts,
// <n> artifacts)`.
func summaryLine(label string, view *runView) string {
	if view == nil {
		return fmt.Sprintf("%s: no result", label)
	}
	return fmt.Sprintf("%s %s finished: %s (%s)",
		label, view.ID, view.Status, counts(view))
}

// counts renders the phase, attempt, and artifact counts with correct plurals.
func counts(view *runView) string {
	artifacts := 0
	for _, phase := range view.Phases {
		artifacts += phase.Artifacts
	}
	return fmt.Sprintf("%s, %s, %s",
		plural(len(view.Phases), "phase"),
		plural(view.Attempts, "attempt"),
		plural(artifacts, "artifact"))
}

// plural renders a count and its noun, pluralising only when it is not one.
func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// deref renders an optional string as "" rather than "<nil>".
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// writeLine is a small helper so renderers do not repeat the nil-writer check.
func writeLine(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format+"\n", args...)
}
