package audit_test

// T055 — artifact integrity (US3 scenario 4). Verification must hold every
// referenced artifact against its recorded sha256, name the file and the expected
// digest when the bytes no longer match, and treat a row whose file is gone as an
// integrity failure rather than a cue to silently re-run the work that produced it
// (contracts/store.md §5 step 3). The exit-code assertions go through the real CLI
// so the mapping from `domain.ErrIntegrity` to exit `6` is what a script sees.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/app"
	"github.com/prompt-fu/zamnius/internal/audit"
	"github.com/prompt-fu/zamnius/internal/cli"
	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/internal/observability"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// TestVerifyArtifactsMatchACleanRun is the baseline: every artifact row a real run
// wrote points at bytes whose digest is the recorded sha256, the recorded size is
// the file size, and verification is therefore a real check with something to
// compare rather than a constant success.
func TestVerifyArtifactsMatchACleanRun(t *testing.T) {
	f := newAuditFixture(t)
	run := f.record(f.threePhaseWorkflow())
	ctx := context.Background()

	refs, err := f.engine.Artifacts(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(refs) < 3 {
		t.Fatalf("run committed %d artifact(s), want the phase, aggregate, and phase outputs", len(refs))
	}

	for _, ref := range refs {
		if err := f.artifacts.Verify(ctx, ref.SHA256); err != nil {
			t.Errorf("artifact %s (%s) does not verify: %v", ref.SHA256, ref.RelativePath, err)
			continue
		}

		// The recorded relative path is the file an operator will find: read it
		// through the state directory rather than the store's private root, so a
		// row that names the wrong location fails here.
		content, readErr := os.ReadFile(filepath.Join(f.dir, ref.RelativePath))
		if readErr != nil {
			t.Errorf("artifact %s is not at its recorded path %s: %v", ref.SHA256, ref.RelativePath, readErr)
			continue
		}
		if !domain.VerifyDigest(ref.SHA256, content) {
			t.Errorf("artifact file %s does not hash to its recorded digest %s", ref.RelativePath, ref.SHA256)
		}
		if int64(len(content)) != ref.SizeBytes {
			t.Errorf("artifact %s size = %d bytes, recorded %d", ref.SHA256, len(content), ref.SizeBytes)
		}
	}

	report, err := f.engine.Verify(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.OK() {
		t.Errorf("Verify() reported problems on a clean run: %+v", report.Problems)
	}
	if report.Artifacts != len(refs) {
		t.Errorf("report.Artifacts = %d, want %d", report.Artifacts, len(refs))
	}
}

// TestVerifyReportsACorruptedArtifactWithExitSix pins the operator-visible failure:
// changing the bytes on disk is detected as a digest mismatch naming the file and
// the expected digest, and resuming the run exits `6` instead of continuing with a
// result that no longer matches its record. The run is left failed with no attempt
// re-executed, so a corrupted artifact is never quietly replaced.
func TestVerifyReportsACorruptedArtifactWithExitSix(t *testing.T) {
	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()
	ref := f.artifactOf(run.RunID, "plan")
	ctx := context.Background()

	// Rewrite the file behind the recorded digest: the row still names the original
	// sha256, which is exactly the situation an auditor has to be able to detect.
	if err := os.WriteFile(f.artifacts.Path(ref.SHA256), []byte("deliberately corrupted"), 0o640); err != nil {
		t.Fatalf("corrupt artifact %s: %v", ref.SHA256, err)
	}

	report, err := f.engine.Verify(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	problem := findProblem(report.Problems, audit.ProblemDigestMismatch, ref.SHA256)
	if problem == nil {
		t.Fatalf("Verify() did not report a digest mismatch for %s: %+v", ref.SHA256, report.Problems)
	}
	if !strings.Contains(problem.Detail, ref.RelativePath) {
		t.Errorf("problem detail %q does not name the file %s", problem.Detail, ref.RelativePath)
	}
	if !strings.Contains(problem.Detail, ref.SHA256) {
		t.Errorf("problem detail %q does not name the expected digest %s", problem.Detail, ref.SHA256)
	}

	before := f.events(run.RunID)
	attemptsBefore := f.attemptCount(run.RunID)

	code, stderr := f.resumeViaCLI(run.RunID)
	if code != cli.ExitIntegrity {
		t.Fatalf("run resume exit = %d, want %d (integrity); stderr: %s", code, cli.ExitIntegrity, stderr)
	}
	if !strings.Contains(stderr, ref.RelativePath) {
		t.Errorf("resume diagnostic %q does not name the corrupted file %s", stderr, ref.RelativePath)
	}
	if !strings.Contains(stderr, ref.SHA256) {
		t.Errorf("resume diagnostic %q does not name the expected digest %s", stderr, ref.SHA256)
	}

	after := f.events(run.RunID)
	if added := eventTypesFrom(after, len(before)); len(added) != 1 || added[0] != string(domain.EventRunFailed) {
		t.Errorf("resume appended %v, want exactly [%s]", added, domain.EventRunFailed)
	}
	if got := f.attemptCount(run.RunID); got != attemptsBefore {
		t.Errorf("attempt count = %d after the refusal, want %d (no work re-executed)", got, attemptsBefore)
	}
	if attemptStarted := eventTypesFrom(after, len(before)); contains(attemptStarted, string(domain.EventAttemptStarted)) {
		t.Errorf("resume started new work on corrupted state: %v", attemptStarted)
	}
}

// TestResumeRefusesAMissingArtifactWithoutRerunning pins contracts/store.md §5
// step 3: a row whose file is gone is an integrity failure with evidence, never a
// signal to re-run the phase. The run fails, nothing is executed, and a second
// resume is a no-op because the run is terminal — the record is never rebuilt by
// guesswork.
func TestResumeRefusesAMissingArtifactWithoutRerunning(t *testing.T) {
	f := newAuditFixture(t)
	run := f.pausedAfterFirstPhase()
	ref := f.artifactOf(run.RunID, "plan")
	ctx := context.Background()

	if err := os.Remove(f.artifacts.Path(ref.SHA256)); err != nil {
		t.Fatalf("remove artifact %s: %v", ref.SHA256, err)
	}

	before := f.events(run.RunID)
	attemptsBefore := f.attemptCount(run.RunID)

	result, resumeErr := f.engine.Resume(ctx, run.RunID, engine.ResumeOptions{})
	if resumeErr == nil {
		t.Fatal("Resume() accepted a run whose artifact file is missing")
	}
	if !errors.Is(resumeErr, domain.ErrIntegrity) {
		t.Errorf("Resume() error = %v, want it to wrap ErrIntegrity", resumeErr)
	}
	if result.Status != domain.RunFailed {
		t.Errorf("resumed status = %q, want failed", result.Status)
	}
	if hint := domain.HintOf(resumeErr); !strings.Contains(hint, ref.RelativePath) ||
		!strings.Contains(hint, ref.SHA256) {
		t.Errorf("resume hint %q does not name the file %s and its digest %s", hint, ref.RelativePath, ref.SHA256)
	}

	// The missing file is reported as missing, not as a generic mismatch, so an
	// operator knows whether to restore a backup or investigate an edit.
	report, err := f.engine.Verify(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if problem := findProblem(report.Problems, audit.ProblemMissingFile, ref.SHA256); problem == nil {
		t.Errorf("Verify() did not report a missing file for %s: %+v", ref.SHA256, report.Problems)
	}

	after := f.events(run.RunID)
	if added := eventTypesFrom(after, len(before)); len(added) != 1 || added[0] != string(domain.EventRunFailed) {
		t.Errorf("refused resume appended %v, want exactly [%s]", added, domain.EventRunFailed)
	}
	if got := f.attemptCount(run.RunID); got != attemptsBefore {
		t.Errorf("attempt count = %d after the refusal, want %d (no work re-executed)", got, attemptsBefore)
	}

	// A second resume must change nothing: the run is terminal, so there is no
	// path from a failed integrity check back into execution (FR-018).
	stable := len(f.events(run.RunID))
	if _, err := f.engine.Resume(ctx, run.RunID, engine.ResumeOptions{}); err != nil {
		t.Fatalf("second Resume() error = %v, want a terminal-run no-op", err)
	}
	if got := len(f.events(run.RunID)); got != stable {
		t.Errorf("second resume appended %d event(s), want no change", got-stable)
	}
}

// pausedAfterFirstPhase drives a two-phase workflow and interrupts it once the
// first phase is terminal, so the run is paused with a committed artifact and a
// resumable position — the state a corrupt or deleted file is discovered in.
func (f *auditFixture) pausedAfterFirstPhase() engine.RunResult {
	f.t.Helper()

	def := f.workflow("artifact-integrity", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("plan", agent("plan"), definition.WithOutputs("plan")).
			WithPhase("review", agent("review"), definition.WithOutputs("report"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancelling from the observer, after the phase transition is committed and
	// before the loop advances, makes the interruption deterministic (T053).
	result, err := f.engine.Start(ctx, def, engine.StartOptions{
		Observer: &cancelAfterPhase{Observer: fakes.NewObserver(), phase: "plan", cancel: cancel},
	})
	if err == nil {
		f.t.Fatal("Start() returned no error for an interrupted run")
	}
	if result.Status != domain.RunPaused {
		f.t.Fatalf("interrupted run status = %q, want paused", result.Status)
	}
	return result
}

// artifactOf returns the committed artifact belonging to one phase.
func (f *auditFixture) artifactOf(runID uuid.UUID, phase string) domain.ArtifactRef {
	f.t.Helper()

	refs, err := f.engine.Artifacts(context.Background(), runID)
	if err != nil {
		f.t.Fatalf("Artifacts() error = %v", err)
	}
	for _, ref := range refs {
		if ref.Phase == phase {
			return ref
		}
	}
	f.t.Fatalf("run %s has no artifact for phase %q", runID, phase)
	return domain.ArtifactRef{}
}

// attemptCount reports how many attempts the run has recorded, across phases.
func (f *auditFixture) attemptCount(runID uuid.UUID) int {
	f.t.Helper()

	phases, err := f.store.Phases(context.Background(), runID)
	if err != nil {
		f.t.Fatalf("Phases() error = %v", err)
	}
	total := 0
	for _, phase := range phases {
		attempts, err := f.store.Attempts(context.Background(), runID, phase.Name)
		if err != nil {
			f.t.Fatalf("Attempts(%q) error = %v", phase.Name, err)
		}
		total += len(attempts)
	}
	return total
}

// resumeViaCLI runs `zamnius run resume` in process against the fixture's state
// directory. The model adapter is constructed but never called, because integrity
// is decided before any phase executes.
func (f *auditFixture) resumeViaCLI(runID uuid.UUID) (int, string) {
	f.t.Helper()

	f.t.Setenv(app.BaseURLEnvVar, "http://127.0.0.1:9/v1")
	f.t.Setenv(observability.SecretEnvVar, "sk-test-not-used")
	f.t.Setenv("ZAMNIUS_LOG_LEVEL", "error")

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{
		"run", "resume", runID.String(), "--state-dir", f.dir,
	}, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

// cancelAfterPhase is a recording observer that cancels the run once a named phase
// finishes. The cancel happens before the drive loop advances, so the interrupted
// run is paused at a known position rather than at a scheduler-dependent one.
type cancelAfterPhase struct {
	*fakes.Observer
	phase  string
	cancel context.CancelFunc
}

// OnPhaseFinished records the fact and trips the cancellation.
func (o *cancelAfterPhase) OnPhaseFinished(ctx context.Context, runID uuid.UUID, phase string, status domain.PhaseStatus) {
	o.Observer.OnPhaseFinished(ctx, runID, phase, status)
	if phase == o.phase {
		o.cancel()
	}
}

// findProblem returns the problem of one kind for one digest, if any.
func findProblem(problems []audit.Problem, kind, sha string) *audit.Problem {
	for i := range problems {
		if problems[i].Kind == kind && problems[i].SHA256 == sha {
			return &problems[i]
		}
	}
	return nil
}

// eventTypesFrom lists the types of the events appended after the first n.
func eventTypesFrom(events []domain.Event, n int) []string {
	types := make([]string, 0, len(events)-n)
	for _, event := range events[n:] {
		types = append(types, event.Type)
	}
	return types
}

// contains reports whether want is in values.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
