package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/app"
	"github.com/qi-lore/zamnius/internal/config"
	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/pkg/zamnius"
)

// runFlags are the flags every `run` subcommand shares.
type runFlags struct {
	stateDir string
	config   string
	json     bool
}

// addCommon registers the flags shared by the run subcommands.
func addCommon(set *flag.FlagSet, flags *runFlags) {
	set.StringVar(&flags.stateDir, "state-dir", "", "state directory (default ./.zamnius)")
	set.StringVar(&flags.config, "config", "", "configuration file")
	set.BoolVar(&flags.json, "json", false, "emit the JSON envelope")
}

// openSettings resolves configuration and opens the composition root, returning
// the close function so no command can forget to release the store.
func (r *runner) openSettings(flags runFlags) (*app.App, func(), error) {
	settings, err := r.resolveSettings(flags.stateDir, flags.config)
	if err != nil {
		return nil, nil, err
	}
	return openApp(r.ctx, settings)
}

// openApp opens the composition root for a resolved setting set.
func openApp(ctx context.Context, settings config.Settings) (*app.App, func(), error) {
	composition, err := app.Open(ctx, settings)
	if err != nil {
		return nil, nil, err
	}
	return composition, func() { _ = composition.Close() }, nil
}

// runStart executes `zamnius run start`.
func (r *runner) runStart(args []string) int {
	const command = "run.start"

	positional, flags := splitArgs(args)
	if len(positional) != 0 {
		return r.usage(fmt.Sprintf("run start takes no positional arguments, got %q", positional[0]))
	}

	var file string
	var shared runFlags
	set := r.newFlagSet(command)
	set.StringVar(&file, "file", "", "definition file (.yaml, .yml, .json, .toml)")
	addCommon(set, &shared)
	if ok, code := r.parseFlags(set, flags); !ok {
		return code
	}
	r.json = shared.json

	if strings.TrimSpace(file) == "" {
		return r.usage("--file is required: run start needs a workflow definition")
	}

	settings, err := r.resolveSettings(shared.stateDir, shared.config)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	composition, closeFn, err := openApp(r.ctx, settings)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	defer closeFn()

	def, err := definition.LoadFile(r.ctx, file)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}

	// Executing work needs the model credential, which is why the engine is built
	// only here rather than for inspection commands.
	engine, err := composition.Engine()
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}

	// Human preamble is suppressed under --json: stdout must then carry exactly
	// one object, because a script parses it as a whole.
	if !r.json {
		writeLine(r.stdout, "definition: %s (%s), state: %s",
			def.Name, plural(len(def.Phases), "phase"), composition.Paths.Dir)
	}

	result, startErr := engine.Start(r.ctx, def, zamnius.StartOptions{MaxConcurrency: settings.MaxConcurrency})
	return r.reportOutcome(command, engine, result, startErr)
}

// inspectCommand is the shared preamble of the read-only commands: parse the run
// id and flags, resolve settings, and hand an inspection-only engine to the body.
// Inspection never needs a model credential, so a run stays readable after the key
// that produced it has expired.
func (r *runner) inspectCommand(command string, args []string, body func(*zamnius.Engine, uuid.UUID) int) int {
	positional, flags := splitArgs(args)
	if len(positional) != 1 {
		return r.usage(command + " needs exactly one run id")
	}

	var shared runFlags
	set := r.newFlagSet(command)
	addCommon(set, &shared)
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

	engine, err := composition.Inspector()
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	return body(engine, runID)
}

// reportOutcome renders the result of a command that executed work: the run view
// is read back from the store, so a failed run is still inspectable and every
// command reports the same shape.
func (r *runner) reportOutcome(command string, engine *zamnius.Engine, result zamnius.RunResult, runErr error) int {
	view, refs, viewErr := r.inspect(engine, result.RunID)
	if viewErr != nil {
		return r.finish(command, nil, nil, result.Status, viewErr, nil)
	}
	return r.finish(command, view, artifactViews(refs), result.Status, runErr, func() {
		writeLine(r.stdout, "%s", summaryLine("run", view))
	})
}

// inspect reads the run's current view and artifacts, tolerating a run that was
// never created (Start failed before the first commit) so the caller still reports
// the original error. Reporting is deliberately detached from a context that has
// already ended: a run that stopped because the process was signalled still has to
// be describable, otherwise the operator sees exit `1` instead of the signalled
// exit `7` and no view of what is now paused (contracts/cli.md §3, T052).
func (r *runner) inspect(engine *zamnius.Engine, runID uuid.UUID) (*runView, []zamnius.ArtifactRef, error) {
	if runID == uuid.Nil {
		return nil, nil, nil
	}
	ctx := r.ctx
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	status, err := engine.Status(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	refs, err := engine.Artifacts(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return viewOf(status, refs), refs, nil
}

// alreadyComplete is the human form of a resume that did nothing because the run
// was already terminal (FR-018).
func (r *runner) alreadyComplete(view *runView) {
	writeLine(r.stdout, "run %s is already complete: %s, nothing to do", view.ID, view.Status)
}
