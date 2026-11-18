package recoverability_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
	"github.com/qi-lore/zamnius/internal/store/artifactfs"
	"github.com/qi-lore/zamnius/internal/store/sqlite"
	"github.com/qi-lore/zamnius/test/fakes"
)

// leaseTTL is short on purpose: a crashed run leaves its lease held, and a real
// recovery waits for that lease to lapse. Twenty milliseconds keeps the suite fast
// while still exercising the expiry path rather than a forced release.
const leaseTTL = 20 * time.Millisecond

// answerer is a deterministic model. It answers from the request's attempt label,
// so a re-run of the same step produces byte-identical content, and it can run a
// hook on each call — which is how a fault point is armed only after the phases
// before the target have already committed.
type answerer struct {
	mu     sync.Mutex
	calls  []domain.ModelRequest
	onCall func(domain.ModelRequest)
}

func (a *answerer) Complete(_ context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	a.mu.Lock()
	a.calls = append(a.calls, req)
	hook := a.onCall
	a.mu.Unlock()

	if hook != nil {
		hook(req)
	}
	return domain.ModelResponse{
		Content:    "document for " + req.Metadata["attempt"],
		Usage:      domain.Usage{InputTokens: 5, OutputTokens: 3},
		StopReason: domain.StopEnd,
	}, nil
}

// callsFor counts the calls whose attempt label is the given phase or branch name.
func (a *answerer) callsFor(label string) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	count := 0
	for _, call := range a.calls {
		if call.Metadata["attempt"] == label {
			count++
		}
	}
	return count
}

// registry resolves the one provider a fixture uses. The engine's Models port is
// declared in the engine package, so the adapter has to name that type — exactly
// what the composition root does in production.
type registry struct {
	client engine.Model
}

func (r registry) Client(provider string) (engine.Model, error) {
	if provider == "openai-compatible" {
		return r.client, nil
	}
	return nil, fmt.Errorf("provider %q is not configured", provider)
}

// fixture is one assembled instance over one state directory. Two fixtures over
// the same directory model two processes over the same persisted store.
type fixture struct {
	store     *sqlite.Store
	artifacts *artifactfs.Store
	engine    *engine.Engine
	model     engine.Model
	runID     uuid.UUID
}

// newInstance builds an engine over dir, using ids for identifier allocation so a
// test can predict the run id the engine will assign. It reports its error rather
// than failing a *testing.T, because the re-executed child process in
// sigkill_test.go has no *testing.T to fail — and using the same construction path
// is what makes the process-death test prove the production wiring crosses a real
// process boundary.
func newInstance(dir, owner string, model engine.Model, ids func() uuid.UUID) (*fixture, error) {
	return newInstanceWith(dir, owner, model, ids, nil)
}

// newInstanceWith is newInstance with an optional store decorator. The decorator
// wraps the durable store the engine sees, which is how a test observes or forces
// a lease renewal without changing production wiring (T048).
func newInstanceWith(dir, owner string, model engine.Model, ids func() uuid.UUID, decorate func(engine.Store) engine.Store) (*fixture, error) {
	layout := fakes.StateLayout(dir)
	store, err := sqlite.Open(context.Background(), layout.Database)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	artifactStore, err := artifactfs.Open(layout.Artifact)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open artifact store: %w", err)
	}

	var storePort engine.Store = store
	if decorate != nil {
		storePort = decorate(store)
	}

	eng, err := engine.New(engine.Options{
		Store:     storePort,
		Artifacts: artifactStore,
		Models:    registry{client: model},
		Clock:     fakes.NewClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Millisecond).Now,
		IDSource:  ids,
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Owner:     owner,
		LeaseTTL:  leaseTTL,
	})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("build engine: %w", err)
	}

	return &fixture{store: store, artifacts: artifactStore, engine: eng, model: model}, nil
}

// openInstance builds an engine over dir for a test, closing the store when the
// test ends so no scratch state outlives it.
func openInstance(t *testing.T, dir, owner string, model engine.Model, ids func() uuid.UUID) *fixture {
	t.Helper()

	f, err := newInstance(dir, owner, model, ids)
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	return f
}

// twoPhaseDefinition is a workflow whose first phase commits cleanly and whose
// second phase is where an interruption is injected: it proves that completed work
// survives a crash in later work.
func twoPhaseDefinition(t *testing.T) definition.Definition {
	t.Helper()

	def, err := definition.NewBuilder("recoverable").
		WithPhase("draft", definition.Agent("draft", "write a draft", "gpt-4.1-mini", 128),
			definition.WithOutputs("plan")).
		WithPhase("review", definition.Agent("review", "review the draft", "gpt-4.1-mini", 128),
			definition.WithOutputs("report")).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}
	return def
}

// branchingDefinition is the two-branch fan-out joined by "all". It returns its
// error rather than failing a *testing.T because the re-executed child process in
// sigkill_test.go has no *testing.T, and because the parent's control run and the
// child must be built from one definition so their artifacts are comparable.
func branchingDefinition() (definition.Definition, error) {
	return definition.NewBuilder("branching").
		WithFanout("draft",
			definition.JoinPolicy{Rule: definition.JoinAll},
			[]definition.Branch{
				definition.BranchOf("outline", definition.Agent("outline", "write an outline", "gpt-4.1-mini", 128)),
				definition.BranchOf("details", definition.Agent("details", "write the details", "gpt-4.1-mini", 128)),
			},
			definition.WithOutputs("aggregate")).
		Build()
}

// fanoutDefinition is a two-branch fan-out joined by "all".
func fanoutDefinition(t *testing.T) definition.Definition {
	t.Helper()

	def, err := branchingDefinition()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}
	return def
}

// predictRunID wraps a fresh sequential identifier source and hands back the first
// identifier drawn: Start assigns the run id before it draws anything else, so the
// first draw is the run's identifier. Predicting it lets a test resume the run the
// engine just created.
func predictRunID() (func() uuid.UUID, func() uuid.UUID) {
	var mu sync.Mutex
	ids := fakes.NewIDSource()
	var run uuid.UUID
	next := func() uuid.UUID {
		id := ids()
		mu.Lock()
		if run == uuid.Nil {
			run = id
		}
		mu.Unlock()
		return id
	}
	return next, func() uuid.UUID {
		mu.Lock()
		defer mu.Unlock()
		return run
	}
}

// runControl executes a workflow to completion in dir and returns the fixture
// together with the run id the engine assigned (its first identifier).
func runControl(t *testing.T, dir string, model engine.Model, def definition.Definition) (*fixture, uuid.UUID) {
	t.Helper()

	ids, runID := predictRunID()
	f := openInstance(t, dir, "control-owner", model, ids)

	result, err := f.engine.Start(context.Background(), def, engine.StartOptions{})
	if err != nil {
		t.Fatalf("control Start() error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("control status = %q, want succeeded", result.Status)
	}
	return f, runID()
}

// digestsOf returns the set of artifact digests committed for a run.
func digestsOf(t *testing.T, f *fixture, runID uuid.UUID) map[string]bool {
	t.Helper()

	refs, err := f.store.Artifacts(context.Background(), runID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	out := make(map[string]bool, len(refs))
	for _, ref := range refs {
		out[ref.SHA256] = true
	}
	return out
}

// attemptsOf returns the attempts committed for one phase.
func attemptsOf(t *testing.T, f *fixture, runID uuid.UUID, phase string) []domain.Attempt {
	t.Helper()

	attempts, err := f.store.Attempts(context.Background(), runID, phase)
	if err != nil {
		t.Fatalf("Attempts(%s) error = %v", phase, err)
	}
	return attempts
}
