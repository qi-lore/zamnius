package recoverability_test

// This file covers T049: the definition-drift guard must see the definition the
// caller supplies at resume, not only the run's own snapshot. A workflow edited
// between start and resume is refused unless the operator overrides the drift, and
// even then the engine finishes the workflow the run was created from — committed
// phases were recorded against that snapshot, so a different definition cannot be
// executed halfway through (data-model.md §10).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/test/fakes"
)

// TestResumeRefusesSuppliedDefinitionDrift proves that resuming with a definition
// that differs from the run's recorded fingerprint is refused, that the refusal
// names both fingerprints and the flag that overrides it, and that a refused resume
// records nothing (FR-014, data-model.md §10).
func TestResumeRefusesSuppliedDefinitionDrift(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-supplied-drift", &answerer{}, fakes.NewIDSource())

	recorded := twoPhaseDefinition(t)
	supplied := editedDefinition(t)
	runID := createRunForDefinition(t, f, recorded, mustFingerprint(t, recorded))

	before := eventsOf(t, f, runID)

	_, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{Definition: &supplied})
	if !errors.Is(err, domain.ErrDefinitionDrift) {
		t.Fatalf("Resume() with a drifted definition error = %v, want ErrDefinitionDrift", err)
	}

	hint := domain.HintOf(err)
	if hint == "" {
		t.Fatal("drift refusal carries no hint; the operator cannot act on it")
	}
	for _, fingerprint := range []string{mustFingerprint(t, recorded), mustFingerprint(t, supplied)} {
		if !strings.Contains(hint, fingerprint) {
			t.Errorf("hint %q does not name fingerprint %s", hint, fingerprint)
		}
	}
	if !strings.Contains(hint, "--allow-definition-drift") {
		t.Errorf("hint %q does not tell the operator how to proceed", hint)
	}

	if after := eventsOf(t, f, runID); len(after) != len(before) {
		t.Errorf("a refused resume appended %d events, want 0", len(after)-len(before))
	}
	run, err := f.store.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if run.Status != domain.RunPending {
		t.Errorf("status after a refused resume = %q, want pending (untouched)", run.Status)
	}
}

// TestResumeAcceptsSuppliedDefinitionDriftWithOverride proves the override is not
// silent and not a swap: with --allow-definition-drift the run finishes the
// workflow it was created from, while the decision records both fingerprints so a
// reader can tell why the operator's file and the executed workflow disagreed
// (FR-014, data-model.md §10).
func TestResumeAcceptsSuppliedDefinitionDriftWithOverride(t *testing.T) {
	dir := t.TempDir()
	model := &answerer{}
	f := openInstance(t, dir, "owner-supplied-drift", model, fakes.NewIDSource())

	recorded := twoPhaseDefinition(t)
	supplied := editedDefinition(t)
	runID := createRunForDefinition(t, f, recorded, mustFingerprint(t, recorded))

	result, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{
		Definition:           &supplied,
		AllowDefinitionDrift: true,
	})
	if err != nil {
		t.Fatalf("Resume() with drift overridden error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status with drift overridden = %q, want succeeded", result.Status)
	}

	// The supplied definition added a "publish" phase. It must not run: the engine
	// completes the recorded snapshot, so committed work and the rest of the workflow
	// stay consistent with one another.
	names := make([]string, 0, len(result.Phases))
	for _, phase := range result.Phases {
		names = append(names, phase.Name)
	}
	if want := []string{"draft", "review"}; !slices.Equal(names, want) {
		t.Errorf("executed phases = %v, want the recorded workflow %v", names, want)
	}
	if calls := model.callsFor("publish"); calls != 0 {
		t.Errorf("the supplied definition's extra phase ran %d times, want 0", calls)
	}

	expected := mustFingerprint(t, recorded)
	submitted := mustFingerprint(t, supplied)
	decisions, err := f.store.Decisions(context.Background(), runID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}

	overrides := 0
	for _, decision := range decisions {
		if decision.Kind != domain.DecisionResumeRestart || !strings.Contains(decision.Summary, "drift") {
			continue
		}
		overrides++
		for _, fingerprint := range []string{expected, submitted} {
			if !strings.Contains(decision.Rationale, fingerprint) {
				t.Errorf("override rationale %q does not name fingerprint %s", decision.Rationale, fingerprint)
			}
			if !strings.Contains(string(decision.Evidence), fingerprint) {
				t.Errorf("override evidence %s does not name fingerprint %s", decision.Evidence, fingerprint)
			}
		}
	}
	if overrides != 1 {
		t.Errorf("definition-drift override decisions = %d, want exactly 1", overrides)
	}
}

// TestResumeAcceptsSuppliedDefinitionThatMatches proves a caller may supply the
// workflow it started with — a fresh parse of the same file — without the guard
// firing or a spurious override being recorded.
func TestResumeAcceptsSuppliedDefinitionThatMatches(t *testing.T) {
	dir := t.TempDir()
	f := openInstance(t, dir, "owner-supplied-match", &answerer{}, fakes.NewIDSource())

	recorded := twoPhaseDefinition(t)
	supplied := twoPhaseDefinition(t)
	expected := mustFingerprint(t, recorded)
	if submitted := mustFingerprint(t, supplied); submitted != expected {
		t.Fatalf("fixture fingerprints differ: %s vs %s", expected, submitted)
	}

	runID := createRunForDefinition(t, f, recorded, expected)
	result, err := f.engine.Resume(context.Background(), runID, engine.ResumeOptions{Definition: &supplied})
	if err != nil {
		t.Fatalf("Resume() with a matching definition error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status with a matching definition = %q, want succeeded", result.Status)
	}

	decisions, err := f.store.Decisions(context.Background(), runID)
	if err != nil {
		t.Fatalf("Decisions() error = %v", err)
	}
	for _, decision := range decisions {
		if strings.Contains(decision.Summary, "drift") {
			t.Errorf("a matching definition recorded a drift override: %q", decision.Summary)
		}
	}
}

// editedDefinition is twoPhaseDefinition after an operator added a phase: same
// workflow name, different content, so the fingerprints differ for the only reason
// drift exists — a file changed between start and resume.
func editedDefinition(t *testing.T) definition.Definition {
	t.Helper()

	def, err := definition.NewBuilder("recoverable").
		WithPhase("draft", definition.Agent("draft", "write a draft", "gpt-4.1-mini", 128),
			definition.WithOutputs("plan")).
		WithPhase("review", definition.Agent("review", "review the draft", "gpt-4.1-mini", 128),
			definition.WithOutputs("report")).
		WithPhase("publish", definition.Agent("publish", "publish the report", "gpt-4.1-mini", 128),
			definition.WithOutputs("custom:release")).
		Build()
	if err != nil {
		t.Fatalf("build edited definition: %v", err)
	}
	return def
}
