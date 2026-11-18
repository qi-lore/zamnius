package integration_test

// This file covers T051: `zamnius run resume` is the operator's recovery path, so
// every outcome it can reach is pinned — a resumable run finishes, a terminal run
// is a no-op that needs no credential, a live owner is named with its lease expiry,
// a drifted --file is refused unless overridden, and state written by an
// incompatible schema is refused with the contract's exit code (contracts/cli.md §1,
// §3, §5).

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/internal/observability"
	"github.com/qi-lore/zamnius/internal/store/artifactfs"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
	"github.com/qi-lore/zamnius/test/fakes"
)

// TestCLIResumeCompletesAResumableRun proves the happy path: a run left pending by
// an interrupted process is driven to success by `run resume`, and the command emits
// the same versioned envelope every other command does.
func TestCLIResumeCompletesAResumableRun(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	code, envelope, stderr := env.runJSON("run", "resume", id.String(), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	if got := envelope["api_version"]; got != "1" {
		t.Errorf("api_version = %v, want \"1\"", got)
	}
	if got := envelope["command"]; got != "run.resume" {
		t.Errorf("command = %v, want run.resume", got)
	}
	if got := envelope["ok"]; got != true {
		t.Errorf("ok = %v, want true", got)
	}
	for _, key := range []string{"api_version", "command", "ok", "run", "artifacts", "error"} {
		if _, present := envelope[key]; !present {
			t.Errorf("envelope is missing the %q key", key)
		}
	}

	run, _ := envelope["run"].(map[string]any)
	if got := run["status"]; got != "succeeded" {
		t.Errorf("run.status = %v, want succeeded", got)
	}
	if got, ok := run["attempts"].(float64); !ok || got != 1 {
		t.Errorf("run.attempts = %v, want 1", run["attempts"])
	}
	if len(artifactsOf(t, envelope)) == 0 {
		t.Error("a resumed phase that declares outputs committed no artifact")
	}

	// The resume is a recorded fact, not an implicit restart: the event log names it.
	if types := eventTypesAt(t, env.stateDir, id); indexOfEvent(types, string(domain.EventRunResumed)) < 0 {
		t.Errorf("event log %v has no run.resumed", types)
	}
}

// TestCLIResumeOfAPausedRunContinuesFromItsPosition proves a genuinely interrupted
// run resumes where it stopped: the abandoned attempt keeps its record, the phase
// gets one replacement attempt, and the run completes.
func TestCLIResumeOfAPausedRunContinuesFromItsPosition(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPausedRun(t, env.stateDir, fixture(t, "hello.yaml"))

	before := eventTypesAt(t, env.stateDir, id)
	if indexOfEvent(before, string(domain.EventRunPaused)) < 0 {
		t.Fatalf("seeded run is not paused: %v", before)
	}

	code, envelope, stderr := env.runJSON("run", "resume", id.String(), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	run, _ := envelope["run"].(map[string]any)
	if got := run["status"]; got != "succeeded" {
		t.Fatalf("resumed run.status = %v, want succeeded", got)
	}

	store := openStoreAt(t, env.stateDir)
	attempts, err := store.Attempts(context.Background(), id, "greet")
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts after resume = %d, want 2 (the interrupted attempt plus its replacement)", len(attempts))
	}
	if attempts[0].Status != domain.AttemptAbandoned {
		t.Errorf("interrupted attempt status = %q, want abandoned", attempts[0].Status)
	}
	if attempts[1].Status != domain.AttemptSucceeded {
		t.Errorf("replacement attempt status = %q, want succeeded", attempts[1].Status)
	}
	if types := eventTypesAt(t, env.stateDir, id); indexOfEvent(types, string(domain.EventRunResumed)) < 0 {
		t.Errorf("event log %v has no run.resumed", types)
	}
}

// TestCLIResumeOfATerminalRunDoesNothing pins FR-018 and contracts/cli.md §5.2: a
// terminal run is reported as already complete, exits 0, appends no event, and does
// not even need the model credential, because no work is executed.
func TestCLIResumeOfATerminalRunDoesNothing(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	if code, _, stderr := env.run("run", "resume", id.String(), "--state-dir", env.stateDir); code != 0 {
		t.Fatalf("first resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	before := len(eventsAt(t, env.stateDir, id))

	// Any attempt to execute work would now fail while the credential is absent.
	t.Setenv(observability.SecretEnvVar, "")

	code, stdout, stderr := env.run("run", "resume", id.String(), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("terminal resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "already complete") {
		t.Errorf("terminal resume output = %q, want it to say the run is already complete", stdout)
	}

	if after := len(eventsAt(t, env.stateDir, id)); after != before {
		t.Errorf("terminal resume appended %d events, want none", after-before)
	}
}

// TestCLIResumeWhileAnotherProcessOwnsTheRun pins contracts/cli.md §5.3: a live
// owner is exit 4 and the message names the owner and when its lease lapses, so an
// operator knows whether to wait or stop it.
func TestCLIResumeWhileAnotherProcessOwnsTheRun(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	store := openStoreAt(t, env.stateDir)
	lease, err := store.AcquireLease(context.Background(), id, "live-owner-7", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}

	code, envelope, stderr := env.runJSON("run", "resume", id.String(), "--state-dir", env.stateDir)
	if code != 4 {
		t.Fatalf("owned resume exit = %d, want 4 (stderr: %s)", code, stderr)
	}

	failure, _ := envelope["error"].(map[string]any)
	hint, _ := failure["hint"].(string)
	if !strings.Contains(hint, "live-owner-7") {
		t.Errorf("error.hint = %q, want it to name the live owner", hint)
	}
	if expires := domain.FormatTimestamp(lease.ExpiresAt); !strings.Contains(hint, expires) {
		t.Errorf("error.hint = %q, want it to name the lease expiry %s", hint, expires)
	}
	run, _ := envelope["run"].(map[string]any)
	if got := run["owner_id"]; got != "live-owner-7" {
		t.Errorf("run.owner_id = %v, want live-owner-7", got)
	}

	// A refused resume wrote nothing: the run is still pending with no resume event.
	if types := eventTypesAt(t, env.stateDir, id); indexOfEvent(types, string(domain.EventRunResumed)) >= 0 {
		t.Errorf("owned resume appended run.resumed: %v", types)
	}
	if err := store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
}

// TestCLIResumeRefusesADriftedSuppliedDefinition pins contracts/cli.md §1 and §3: a
// --file whose fingerprint differs from the run's recorded one is exit 5, names both
// fingerprints and the override flag, and leaves the run untouched.
func TestCLIResumeRefusesADriftedSuppliedDefinition(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))
	before := len(eventsAt(t, env.stateDir, id))

	code, envelope, stderr := env.runJSON("run", "resume", id.String(),
		"--state-dir", env.stateDir, "--file", fixture(t, "fanout.yaml"))
	if code != 5 {
		t.Fatalf("drifted resume exit = %d, want 5 (stderr: %s)", code, stderr)
	}

	failure, _ := envelope["error"].(map[string]any)
	hint, _ := failure["hint"].(string)
	if !strings.Contains(hint, "--allow-definition-drift") {
		t.Errorf("error.hint = %q, want it to name the override flag", hint)
	}
	if !strings.Contains(hint, "fingerprint") {
		t.Errorf("error.hint = %q, want it to name the fingerprints", hint)
	}

	after := len(eventsAt(t, env.stateDir, id))
	if after != before {
		t.Errorf("a refused resume appended %d events, want none", after-before)
	}
}

// TestCLIResumeAllowingDriftFinishesTheRecordedWorkflow proves the override is
// recorded and that the run still executes the workflow it was created from: the
// supplied fan-out definition's branches are never run.
func TestCLIResumeAllowingDriftFinishesTheRecordedWorkflow(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	code, envelope, stderr := env.runJSON("run", "resume", id.String(),
		"--state-dir", env.stateDir, "--file", fixture(t, "fanout.yaml"), "--allow-definition-drift")
	if code != 0 {
		t.Fatalf("override resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	run, _ := envelope["run"].(map[string]any)
	if got := run["status"]; got != "succeeded" {
		t.Fatalf("run.status = %v, want succeeded", got)
	}
	phases, _ := run["phases"].([]any)
	if len(phases) != 1 {
		t.Fatalf("run.phases = %d, want the one recorded phase", len(phases))
	}
	if phase, _ := phases[0].(map[string]any); phase["name"] != "greet" {
		t.Errorf("executed phase = %v, want greet from the recorded workflow", phase["name"])
	}

	store := openStoreAt(t, env.stateDir)
	decisions, err := store.Decisions(context.Background(), id)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	overrides := 0
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionResumeRestart {
			overrides++
		}
	}
	if overrides != 1 {
		t.Errorf("drift-override decisions = %d, want exactly 1", overrides)
	}
}

// TestCLIResumeAcceptsAMatchingSuppliedDefinition proves --file is silent when the
// operator's file still matches: no override is recorded and the resume completes.
func TestCLIResumeAcceptsAMatchingSuppliedDefinition(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	code, envelope, stderr := env.runJSON("run", "resume", id.String(),
		"--state-dir", env.stateDir, "--file", fixture(t, "hello.yaml"))
	if code != 0 {
		t.Fatalf("matching --file resume exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if run, _ := envelope["run"].(map[string]any); run["status"] != "succeeded" {
		t.Errorf("run.status = %v, want succeeded", run["status"])
	}

	store := openStoreAt(t, env.stateDir)
	decisions, err := store.Decisions(context.Background(), id)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	for _, decision := range decisions {
		if decision.Kind == domain.DecisionResumeRestart {
			t.Errorf("a matching definition recorded a drift override: %v", decision)
		}
	}
}

// TestCLIResumeRefusesNewerStateWithExitSix pins the carried-in mapping from T050:
// state written by an incompatible schema is a persistence failure (exit 6), names
// the schema that wrote it, and leaves the run pending.
func TestCLIResumeRefusesNewerStateWithExitSix(t *testing.T) {
	env := newCLIEnv(t)
	id := seedPendingRun(t, env.stateDir, fixture(t, "hello.yaml"))

	future := domain.SchemaVersion + 1
	store := openStoreAt(t, env.stateDir)
	if _, err := store.DB().ExecContext(context.Background(),
		"UPDATE runs SET schema_version = ? WHERE id = ?", future, id); err != nil {
		t.Fatalf("forge schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	code, envelope, stderr := env.runJSON("run", "resume", id.String(), "--state-dir", env.stateDir)
	if code != 6 {
		t.Fatalf("newer-state resume exit = %d, want 6 (stderr: %s)", code, stderr)
	}
	failure, _ := envelope["error"].(map[string]any)
	hint, _ := failure["hint"].(string)
	if !strings.Contains(hint, strconv.Itoa(future)) {
		t.Errorf("error.hint = %q, want it to name the schema %d", hint, future)
	}

	reopened := openStoreAt(t, env.stateDir)
	run, err := reopened.LoadRun(context.Background(), id)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Status != domain.RunPending {
		t.Errorf("status after a refused resume = %q, want pending", run.Status)
	}
}

// seedEngine assembles an engine over an existing state directory, the way the
// composition root does, so a test can put durable state in place before a command
// runs.
func seedEngine(t *testing.T, dir string, model engine.Model) (*engine.Engine, *sqlite.Store) {
	t.Helper()

	layout := fakes.StateLayout(dir)
	if err := os.MkdirAll(layout.Artifact, 0o750); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	store := openStoreAt(t, dir)
	artifacts, err := artifactfs.Open(layout.Artifact)
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}

	eng, err := engine.New(engine.Options{
		Store:     store,
		Artifacts: artifacts,
		Models:    staticRegistry{client: model},
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Owner:     "seed-process",
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	return eng, store
}

// seedPendingRun records a run with no work done, which is what an interrupted
// process leaves once it has created the run but executed no phase.
func seedPendingRun(t *testing.T, dir, file string) uuid.UUID {
	t.Helper()

	def := loadDefinition(t, file)
	snapshot, err := definition.Snapshot(def)
	if err != nil {
		t.Fatalf("snapshot definition: %v", err)
	}
	fingerprint, err := definition.Fingerprint(def)
	if err != nil {
		t.Fatalf("fingerprint definition: %v", err)
	}
	phases := make([]domain.PhaseState, 0, len(def.Phases))
	for i, phase := range def.Phases {
		phases = append(phases, domain.PhaseState{Name: phase.Name, Ordinal: i, Status: domain.PhasePending})
	}

	store := openStoreAt(t, dir)
	runID := uuid.New()
	if _, err := store.CreateRun(context.Background(), domain.RunSpec{
		ID:                    runID,
		DefinitionName:        def.Name,
		DefinitionFingerprint: fingerprint,
		DefinitionJSON:        snapshot,
		Phases:                phases,
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}
	return runID
}

// seedPausedRun records a genuinely interrupted run: the scripted model reports the
// call as cancelled, so the engine leaves the run paused with the attempt boundary
// committed.
func seedPausedRun(t *testing.T, dir, file string) uuid.UUID {
	t.Helper()

	def := loadDefinition(t, file)
	model := fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassCancelled,
			Msg:   "the process was interrupted while the provider was answering",
			Err:   context.Canceled,
		}
	})
	eng, store := seedEngine(t, dir, model)

	result, err := eng.Start(context.Background(), def, engine.StartOptions{})
	if err == nil {
		t.Fatal("Start() returned no error for an interrupted run")
	}
	if result.Status != domain.RunPaused {
		t.Fatalf("seeded run status = %q, want paused", result.Status)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}
	return result.RunID
}

// loadDefinition parses a workflow fixture the way the CLI does.
func loadDefinition(t *testing.T, file string) definition.Definition {
	t.Helper()

	def, err := definition.LoadFile(context.Background(), file)
	if err != nil {
		t.Fatalf("load definition %s: %v", file, err)
	}
	return def
}

// openStoreAt opens the state store of a CLI environment so a test can seed or
// inspect durable state directly. The store is closed automatically.
func openStoreAt(t *testing.T, dir string) *sqlite.Store {
	t.Helper()

	store, err := sqlite.Open(context.Background(), fakes.StateLayout(dir).Database)
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// eventsAt reads a run's event log from a closed state directory.
func eventsAt(t *testing.T, dir string, runID uuid.UUID) []domain.Event {
	t.Helper()

	store := openStoreAt(t, dir)
	events, err := store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	return events
}

// eventTypesAt lists a run's event types in seq order.
func eventTypesAt(t *testing.T, dir string, runID uuid.UUID) []string {
	t.Helper()

	types := make([]string, 0)
	for _, event := range eventsAt(t, dir, runID) {
		types = append(types, event.Type)
	}
	return types
}
