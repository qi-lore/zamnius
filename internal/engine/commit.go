package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// commitRun persists a run-level change with its events and decisions.
func (s *session) commitRun(ctx context.Context, delta domain.RunDelta, events []domain.EventDraft, decisions []domain.DecisionDraft, guard bool) error {
	commit := domain.RunCommit{RunID: s.run.ID, Delta: delta, Events: events, Decisions: decisions}
	if guard {
		// Start and pause are single-writer moments, so the version guard is
		// worth having: it catches a second process that advanced the run.
		commit.ExpectedVersion = s.run.RecordVersion
	}
	if err := s.engine.store.CommitRunResult(ctx, commit); err != nil {
		return err
	}
	return s.refresh(ctx)
}

// commitPhase persists a phase transition with an optional run delta.
func (s *session) commitPhase(ctx context.Context, phase domain.PhaseState, delta *domain.RunDelta, events []domain.EventDraft, decisions []domain.DecisionDraft, artifacts ...domain.ArtifactRef) error {
	if err := s.engine.store.CommitPhaseResult(ctx, domain.PhaseCommit{
		Phase:     phase,
		Run:       delta,
		Artifacts: artifacts,
		Events:    events,
		Decisions: decisions,
	}); err != nil {
		return err
	}
	return s.refresh(ctx)
}

// commitAttempt persists one attempt outcome with its artifacts, events and
// decisions. Attempt rows are written without touching the phase row, because
// fan-out branches commit concurrently and a shared row update would contend.
func (s *session) commitAttempt(ctx context.Context, attempt domain.Attempt, artifacts []domain.ArtifactRef, events []domain.EventDraft, decisions []domain.DecisionDraft) error {
	return s.engine.store.CommitAttemptResult(ctx, domain.AttemptCommit{
		Attempt:   attempt,
		Artifacts: artifacts,
		Events:    events,
		Decisions: decisions,
	})
}

// refresh reloads the run so its record_version and spend reflect what was just
// committed. Reading after a write inside the same call is cheap and keeps the
// in-memory view from drifting.
func (s *session) refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, err := s.engine.store.LoadRun(ctx, s.run.ID)
	if err != nil {
		return err
	}
	s.run = run
	return nil
}

// addSpend accumulates consumption and returns the delta to commit. Tokens are
// real even when they alone exceed a budget, so they are always recorded
// (contracts/model-client.md §5).
func (s *session) addSpend(delta domain.Consumption) domain.Consumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spent = s.spent.Add(delta)
	return delta
}

// spentNow reports the accumulated spend of this session including anything read
// from the store at load time.
func (s *session) spentNow() domain.Consumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent
}

// phaseState returns the last known persisted state of a phase.
func (s *session) phaseState(ctx context.Context, name string) (domain.PhaseState, error) {
	phases, err := s.engine.store.Phases(ctx, s.run.ID)
	if err != nil {
		return domain.PhaseState{}, err
	}
	for _, phase := range phases {
		if phase.Name == name {
			return phase, nil
		}
	}
	return domain.PhaseState{}, fmt.Errorf("phase %q is not part of run %s: %w", name, s.run.ID, domain.ErrNotFound)
}

// nextAttemptNo allocates the next 1-based attempt number for a phase. It is
// monotonic and never reused, which is what makes a resumed attempt traceable
// (data-model.md §4 invariant 3).
func (s *session) nextAttemptNo(phase string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptNo[phase]++
	return s.attemptNo[phase]
}

// loadPhaseKinds records how many attempts each phase already has, so a resumed
// run continues numbering instead of colliding with a committed attempt.
func (s *session) loadPhaseKinds() {
	s.attemptNo = make(map[string]int, len(s.def.Phases))
	s.results = make(map[string]PhaseResult, len(s.def.Phases))
	s.spent = s.run.Spent
}

// seedAttemptNumbering reads committed attempts so resume continues the sequence.
func (s *session) seedAttemptNumbering(ctx context.Context) error {
	for _, phase := range s.def.Phases {
		attempts, err := s.engine.store.Attempts(ctx, s.run.ID, phase.Name)
		if err != nil {
			return err
		}
		highest := 0
		for _, attempt := range attempts {
			if attempt.AttemptNo > highest {
				highest = attempt.AttemptNo
			}
			recordArtifacts(s.kinds, s.artifacts, phase.Name, attempt.Artifacts)
		}
		s.mu.Lock()
		s.attemptNo[phase.Name] = highest
		s.mu.Unlock()
	}
	return nil
}

// recordArtifacts remembers produced kinds so the outputs invariant can be
// checked before a phase is allowed to succeed.
func recordArtifacts(kinds map[string]map[string]struct{}, refs map[string][]domain.ArtifactRef, phase string, artifacts []domain.ArtifactRef) {
	if len(artifacts) == 0 {
		return
	}
	if kinds[phase] == nil {
		kinds[phase] = make(map[string]struct{}, len(artifacts))
	}
	for _, artifact := range artifacts {
		kinds[phase][artifact.Kind] = struct{}{}
		refs[phase] = append(refs[phase], artifact)
	}
}

// upstreamDigests collects the artifact hashes a phase depends on, which makes the
// input fingerprint sensitive to real upstream results (FR-039).
func (s *session) upstreamDigests(ctx context.Context, phase definition.Phase) ([]string, error) {
	digests := make([]string, 0)
	for _, dependency := range phase.DependsOn {
		attempts, err := s.engine.store.Attempts(ctx, s.run.ID, dependency)
		if err != nil {
			return nil, err
		}
		for _, attempt := range attempts {
			for _, artifact := range attempt.Artifacts {
				digests = append(digests, artifact.SHA256)
			}
		}
	}
	return digests, nil
}

// pauseContext is used for commits that happen after the caller's context ended:
// the run must still record why it stopped (FR-008).
func pauseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
