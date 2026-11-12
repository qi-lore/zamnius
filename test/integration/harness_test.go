package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/engine"
	"github.com/hIMEI29A/zamnius/internal/store/artifactfs"
	"github.com/hIMEI29A/zamnius/internal/store/sqlite"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

// harness bundles everything a run needs, assembled exactly the way the
// composition root assembles it: a real SQLite store, a real content-addressed
// artifact store, and a scripted model that never touches the network.
type harness struct {
	t         *testing.T
	engine    *engine.Engine
	store     *sqlite.Store
	artifacts *artifactfs.Store
	client    *fakes.FuncClient
	observer  *fakes.Observer
	clock     *fakes.Clock
}

// newHarness builds an engine whose answers come from answer().
func newHarness(t *testing.T, answer func(domain.ModelRequest) (domain.ModelResponse, error)) *harness {
	t.Helper()
	return newHarnessWithModel(t, fakes.NewFuncClient(answer))
}

// newHarnessWithModel builds an engine over an arbitrary model port. It exists for
// the cancellation suite, whose scripted model must observe its call context rather
// than answer from the request alone.
func newHarnessWithModel(t *testing.T, model engine.Model) *harness {
	t.Helper()

	layout := fakes.StateLayout(fakes.TempStateDir(t))

	store, err := sqlite.Open(context.Background(), layout.Database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	artifactStore, err := artifactfs.Open(layout.Artifact)
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}

	funcClient, _ := model.(*fakes.FuncClient)
	observer := fakes.NewObserver()
	clock := fakes.NewClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Millisecond)

	eng, err := engine.New(engine.Options{
		Store:     store,
		Artifacts: artifactStore,
		Models:    staticRegistry{client: model},
		Clock:     clock.Now,
		IDSource:  fakes.NewIDSource(),
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Owner:     "test-owner",
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	return &harness{
		t:         t,
		engine:    eng,
		store:     store,
		artifacts: artifactStore,
		client:    funcClient,
		observer:  observer,
		clock:     clock,
	}
}

// staticRegistry resolves the only provider the fixtures use. The engine's Models
// port is declared in the engine package, so the adapter has to name that type;
// that is exactly what the composition root does in production.
type staticRegistry struct {
	client engine.Model
}

func (r staticRegistry) Client(provider string) (engine.Model, error) {
	if provider == "openai-compatible" {
		return r.client, nil
	}
	return nil, fmt.Errorf("no client for provider %q: %w", provider, domain.ErrNotFound)
}

// workflow builds a definition through the builder, so both entry points agree.
func (h *harness) workflow(name string, phases func(*definition.Builder) *definition.Builder, options ...func(*definition.Builder) *definition.Builder) definition.Definition {
	h.t.Helper()
	builder := definition.NewBuilder(name)
	for _, option := range options {
		builder = option(builder)
	}
	if phases != nil {
		builder = phases(builder)
	}
	def, err := builder.Build()
	if err != nil {
		h.t.Fatalf("build definition %q: %v", name, err)
	}
	return def
}

// agent is the common test agent.
func agent(name string) definition.AgentRef {
	return definition.Agent(name, "do "+name, "gpt-4.1-mini", 256)
}

// run starts a definition and fails the test on an unexpected error.
func (h *harness) run(def definition.Definition) engine.RunResult {
	h.t.Helper()
	result, err := h.engine.Start(context.Background(), def, engine.StartOptions{Observer: h.observer})
	if err != nil {
		h.t.Fatalf("Start() error = %v", err)
	}
	return result
}

// events reads the whole event log of a run.
func (h *harness) events(runID uuid.UUID) []domain.Event {
	h.t.Helper()
	events, err := h.store.Events(context.Background(), runID, 0)
	if err != nil {
		h.t.Fatalf("Events() error = %v", err)
	}
	return events
}

// eventTypes lists event types in seq order.
func (h *harness) eventTypes(runID uuid.UUID) []string {
	types := make([]string, 0)
	for _, event := range h.events(runID) {
		types = append(types, event.Type)
	}
	return types
}

// indexOfEvent returns the position of the first event of a type, or -1.
func indexOfEvent(types []string, want string) int {
	for i, got := range types {
		if got == want {
			return i
		}
	}
	return -1
}

// ok returns a successful response with usage, keyed by nothing.
func ok(content string, tokensIn, tokensOut int) domain.ModelResponse {
	return domain.ModelResponse{
		Content:    content,
		Usage:      domain.Usage{InputTokens: tokensIn, OutputTokens: tokensOut},
		StopReason: domain.StopEnd,
	}
}

// fakesError builds a classified error, so a script keeps its class explicit.
func fakesError(class domain.ErrorClass, message string) error {
	return &domain.Error{Class: class, Msg: message}
}
