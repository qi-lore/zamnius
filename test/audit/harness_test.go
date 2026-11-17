package audit_test

// Shared fixtures for the audit suites (T054–T057). They assemble a real SQLite
// store, a real content-addressed artifact store, and a real engine exactly the way
// the composition root does, so every assertion is made against records a real run
// wrote rather than against hand-built rows (constitution Principle III: the audit
// record is the only source of truth, so the tests must read what the writer wrote).

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/internal/store/artifactfs"
	"github.com/prompt-fu/zamnius/internal/store/sqlite"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// auditFixture bundles the real collaborators an audit test reads from.
type auditFixture struct {
	t         *testing.T
	dir       string
	engine    *engine.Engine
	store     *sqlite.Store
	artifacts *artifactfs.Store
	client    engine.Model
}

// newAuditFixture builds a fixture whose model always succeeds. It is the common
// case: a workflow that completes normally still has to produce a complete record.
func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()

	// Content is keyed on the phase or branch the call belongs to, so a fan-out's
	// branches produce distinct bytes (and therefore distinct content addresses)
	// rather than deduping to one row.
	return newAuditFixtureWithModel(t, fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{
			Content:    "output of " + req.Metadata["attempt"],
			Usage:      domain.Usage{InputTokens: 7, OutputTokens: 3},
			StopReason: domain.StopEnd,
		}, nil
	}))
}

// newAuditFixtureWithModel builds an isolated state directory and wires the same
// three collaborators production wires, with a deterministic clock and id source so
// a run's identifiers and attempt numbering are reproducible. Callers supply the
// model so a scenario can script the failure that drives a retry, an escalation, or
// a budget stop; every other collaborator stays real.
func newAuditFixtureWithModel(t *testing.T, client engine.Model) *auditFixture {
	t.Helper()

	layout := fakes.StateLayout(fakes.TempStateDir(t))

	store, err := sqlite.Open(context.Background(), layout.Database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	artifacts, err := artifactfs.Open(layout.Artifact)
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}

	eng, err := engine.New(engine.Options{
		Store:     store,
		Artifacts: artifacts,
		Models:    auditRegistry{client: client},
		Clock:     fakes.NewClock(time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC), time.Millisecond).Now,
		IDSource:  fakes.NewIDSource(),
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Owner:     "audit-fixture",
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	return &auditFixture{
		t:         t,
		dir:       layout.Dir,
		engine:    eng,
		store:     store,
		artifacts: artifacts,
		client:    client,
	}
}

// auditRegistry resolves the one provider the fixtures use. The engine's Models port
// is declared in the engine package, so the adapter has to name that type; that is
// exactly what the composition root does in production.
type auditRegistry struct{ client engine.Model }

func (r auditRegistry) Client(provider string) (engine.Model, error) {
	if provider == "openai-compatible" {
		return r.client, nil
	}
	return nil, &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "no client for provider " + provider,
	}
}

// workflow builds a definition through the builder, so the programmatic entry point
// produces exactly what the declarative one would (FR-061).
func (f *auditFixture) workflow(name string, phases func(*definition.Builder) *definition.Builder) definition.Definition {
	f.t.Helper()
	def, err := phases(definition.NewBuilder(name)).Build()
	if err != nil {
		f.t.Fatalf("build definition %q: %v", name, err)
	}
	return def
}

// record starts a definition and fails the test on an unexpected error.
func (f *auditFixture) record(def definition.Definition) engine.RunResult {
	f.t.Helper()
	result, err := f.engine.Start(context.Background(), def, engine.StartOptions{})
	if err != nil {
		f.t.Fatalf("Start() error = %v", err)
	}
	return result
}

// events reads a run's whole event log, in seq order.
func (f *auditFixture) events(runID uuid.UUID) []domain.Event {
	f.t.Helper()
	events, err := f.store.Events(context.Background(), runID, 0)
	if err != nil {
		f.t.Fatalf("Events() error = %v", err)
	}
	return events
}

// decisions reads a run's whole decision record set, in insertion order.
func (f *auditFixture) decisions(runID uuid.UUID) []domain.Decision {
	f.t.Helper()

	decisions, err := f.store.Decisions(context.Background(), runID)
	if err != nil {
		f.t.Fatalf("Decisions() error = %v", err)
	}
	return decisions
}

// eventTypes lists a run's event types in seq order.
func (f *auditFixture) eventTypes(runID uuid.UUID) []string {
	types := make([]string, 0)
	for _, event := range f.events(runID) {
		types = append(types, event.Type)
	}
	return types
}

// agent builds the common test agent.
func agent(name string) definition.AgentRef {
	return definition.Agent(name, "do "+name, "gpt-4.1-mini", 256)
}

// threePhaseWorkflow is the shared clean run: an ordered phase, a three-branch
// fan-out joined with `all`, and a final ordered phase. It produces a log long
// enough to mutate or gap at any interior position.
func (f *auditFixture) threePhaseWorkflow() definition.Definition {
	return f.workflow("audited", func(b *definition.Builder) *definition.Builder {
		return b.
			WithPhase("plan", agent("plan"), definition.WithOutputs("plan")).
			WithFanout("build", definition.JoinPolicy{Rule: definition.JoinAll},
				[]definition.Branch{
					definition.BranchOf("one", agent("one")),
					definition.BranchOf("two", agent("two")),
					definition.BranchOf("three", agent("three")),
				}, definition.WithOutputs("aggregate")).
			WithPhase("review", agent("review"), definition.WithOutputs("report"))
	})
}
