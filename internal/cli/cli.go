// Package cli is the command layer: it routes subcommands, parses flags, renders
// one result, and maps a classified error to a process exit code. It holds no
// business logic — every command is a thin adapter over pkg/zamnius (FR-060).
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hIMEI29A/zamnius/internal/config"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Version is the release of this build. The public contract set is versioned
// independently: see ApiVersion.
const Version = "0.1.0"

// ApiVersion is the version of the --json envelope (contracts/cli.md §2).
const ApiVersion = "1"

// Exit codes, from contracts/cli.md §3. They are the contract tests assert
// against, so they are named rather than written as literals.
const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitRunFailed = 3
	ExitOwned     = 4
	ExitInvalid   = 5
	ExitIntegrity = 6
	ExitCancelled = 7
)

// usage is the one-line reminder printed after a usage error.
const usage = `usage:
  zamnius run start    --file <path> [--state-dir <dir>] [--config <path>] [--json]
  zamnius run resume   <run-id> [--state-dir <dir>] [--config <path>] [--allow-definition-drift] [--json]
  zamnius run status   <run-id> [--state-dir <dir>] [--json]
  zamnius run artifacts <run-id> [--state-dir <dir>] [--out <dir>] [--json]
  zamnius version`

// Run executes one command line and returns the process exit code. Output is
// written to the given writers so the whole surface is testable in process: the
// binary only supplies os.Args, os.Stdout, os.Stderr, and a signal-bound context
// (T041, T052).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	runner := &runner{ctx: ctx, stdout: stdout, stderr: stderr}

	if len(args) == 0 {
		return runner.usage("no command given")
	}

	switch args[0] {
	case "version":
		return runner.version()
	case "run":
		return runner.runCommand(args[1:])
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return ExitOK
	default:
		return runner.usage(fmt.Sprintf("unknown command %q", args[0]))
	}
}

// runner carries everything a command needs. It is deliberately a value built per
// call: no package-level state, so two runs in one process cannot interfere.
type runner struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	// json is set once the flags are parsed, because the renderer is shared by
	// every command and must not re-parse anything.
	json bool
}

// version prints the build and the envelope version it speaks.
func (r *runner) version() int {
	fmt.Fprintf(r.stdout, "zamnius %s (api_version %q, schema %d)\n", Version, ApiVersion, domain.SchemaVersion)
	return ExitOK
}

// runCommand routes `zamnius run <subcommand>`.
func (r *runner) runCommand(args []string) int {
	if len(args) == 0 {
		return r.usage("run requires a subcommand")
	}

	subcommand, rest := args[0], args[1:]
	switch subcommand {
	case "start":
		return r.runStart(rest)
	case "status":
		return r.runStatus(rest)
	case "artifacts":
		return r.runArtifacts(rest)
	case "resume":
		return r.runResume(rest)
	default:
		return r.usage(fmt.Sprintf("unknown run subcommand %q", subcommand))
	}
}

// usage reports a command-line mistake, which is exit 2 by contract.
func (r *runner) usage(message string) int {
	fmt.Fprintf(r.stderr, "zamnius: %s\n%s\n", message, usage)
	return ExitUsage
}

// newFlagSet builds a parser that reports mistakes the way the CLI does: the
// message goes to stderr and the caller decides the exit code.
func (r *runner) newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(r.stderr)
	set.Usage = func() {
		fmt.Fprintln(r.stderr, usage)
	}
	return set
}

// parseFlags parses args into set, mapping every parser outcome onto an exit code.
// It reports whether the caller should continue.
func (r *runner) parseFlags(set *flag.FlagSet, args []string) (bool, int) {
	err := set.Parse(args)
	switch {
	case err == nil:
		return true, ExitOK
	case errors.Is(err, flag.ErrHelp):
		// -h is a request, not a mistake: print the usage and succeed.
		fmt.Fprintln(r.stdout, usage)
		return false, ExitOK
	default:
		fmt.Fprintln(r.stderr, usage)
		return false, ExitUsage
	}
}

// valueFlags are the flags that consume the next argument. Knowing them is what
// lets the single positional argument be found wherever it appears on the line,
// which keeps `run status <id> --json` and `run status --json <id>` equivalent.
var valueFlags = map[string]bool{
	"--file": true, "-file": true,
	"--state-dir": true, "-state-dir": true,
	"--config": true, "-config": true,
	"--out": true, "-out": true,
}

// splitArgs separates positional arguments from flags and their values.
func splitArgs(args []string) (positional, flags []string) {
	for i := 0; i < len(args); i++ {
		token := args[i]
		if !strings.HasPrefix(token, "-") || token == "-" {
			positional = append(positional, token)
			continue
		}
		flags = append(flags, token)
		if valueFlags[token] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return positional, flags
}

// resolveSettings applies the mandated precedence: flags override the environment,
// the environment overrides --config, --config overrides a discovered file, and
// built-in defaults fill the rest (FR-051).
func (r *runner) resolveSettings(stateDir, configPath string) (config.Settings, error) {
	overrides := config.Overrides{}
	if stateDir != "" {
		overrides.StateDir = &stateDir
	}
	workingDir, err := os.Getwd()
	if err != nil {
		workingDir = ""
	}
	return config.Load(r.ctx, config.Sources{
		Overrides:    overrides,
		Environ:      os.Environ(),
		ExplicitFile: configPath,
		DiscoverDir:  workingDir,
	})
}

// exitCodeFor maps a classified error onto the exit-code table. The run's status
// is consulted last: a run that reached `failed` is exit 3 even when the error
// itself carries no more specific classification.
func exitCodeFor(err error, runStatus domain.RunStatus) int {
	if err == nil {
		return ExitOK
	}
	switch {
	case errors.Is(err, domain.ErrInvalidDefinition), errors.Is(err, domain.ErrInvalidConfig):
		return ExitInvalid
	case errors.Is(err, domain.ErrRunAlreadyOwned):
		return ExitOwned
	case errors.Is(err, domain.ErrIntegrity):
		return ExitIntegrity
	}
	if class := domain.ClassOf(err); class == domain.ErrClassCancelled {
		return ExitCancelled
	}
	if runStatus == domain.RunFailed {
		return ExitRunFailed
	}
	return ExitError
}
