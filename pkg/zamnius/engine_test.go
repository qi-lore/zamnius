package zamnius_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/store/artifactfs"
	"github.com/hIMEI29A/zamnius/internal/store/sqlite"
	"github.com/hIMEI29A/zamnius/pkg/zamnius"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

// registry is what an adopter writes: it names the public client type, not the
// engine's port, which is exactly the shape the adapter has to bridge.
type registry struct {
	provider string
	client   zamnius.ModelClient
}

func (r registry) Client(provider string) (zamnius.ModelClient, error) {
	if provider != r.provider {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "no client for provider " + provider,
			Hint:  "known providers: " + r.provider,
		}
	}
	return r.client, nil
}

// adoptable assembles the engine the way an embedded adopter does: real store,
// real artifact store, injected deterministic clock and ids.
func adoptable(t *testing.T, answer func(domain.ModelRequest) (domain.ModelResponse, error)) *zamnius.Engine {
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

	clock := fakes.NewClock(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Millisecond)
	eng, err := zamnius.New(zamnius.Options{
		Store:     store,
		Artifacts: artifactStore,
		Models:    registry{provider: "openai-compatible", client: fakes.NewFuncClient(answer)},
		Clock:     clock.Now,
		IDSource:  fakes.NewIDSource(),
	})
	if err != nil {
		t.Fatalf("zamnius.New() error = %v", err)
	}
	return eng
}

func TestNewRefusesMissingDependencies(t *testing.T) {
	t.Parallel()

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

	cases := []struct {
		name string
		opts zamnius.Options
		want string
	}{
		{"store", zamnius.Options{Artifacts: artifacts, Models: registry{provider: "openai-compatible"}}, "state store"},
		{"artifacts", zamnius.Options{Store: store, Models: registry{provider: "openai-compatible"}}, "artifact store"},
		{"models", zamnius.Options{Store: store, Artifacts: artifacts}, "model registry"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			eng, err := zamnius.New(tc.opts)
			if err == nil {
				t.Fatalf("New() accepted options without a %s", tc.name)
			}
			if eng != nil {
				t.Errorf("New() returned an engine alongside the error")
			}
			var classified *zamnius.Error
			if !errors.As(err, &classified) {
				t.Fatalf("error %v is not a *zamnius.Error", err)
			}
			if classified.Class != zamnius.ErrClassPermanent {
				t.Errorf("class = %q, want permanent", classified.Class)
			}
			if classified.Hint == "" {
				t.Error("a missing dependency must carry a hint naming what to pass")
			}
			if !strings.Contains(classified.Msg, tc.want) {
				t.Errorf("message %q does not name the missing %s", classified.Msg, tc.want)
			}
		})
	}
}

func TestPublicSurfaceDrivesAndReportsARun(t *testing.T) {
	t.Parallel()

	eng := adoptable(t, func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{
			Content:    "output of " + req.Metadata["phase"],
			Usage:      domain.Usage{InputTokens: 4, OutputTokens: 2},
			StopReason: domain.StopEnd,
		}, nil
	})

	observer := fakes.NewObserver()
	def, err := definition.NewBuilder("public-surface").
		WithPhase("draft", definition.Agent("writer", "write it", "gpt-4.1-mini", 256), definition.WithOutputs("plan")).
		WithPhase("review", definition.Agent("critic", "review it", "gpt-4.1-mini", 256), definition.WithOutputs("report")).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}

	result, err := eng.Start(context.Background(), def, zamnius.StartOptions{Observer: observer})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Status != zamnius.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if result.Err != nil {
		t.Errorf("result.Err = %v, want nil on success", result.Err)
	}
	if len(result.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(result.Phases))
	}
	if result.Phases[0].Name != "draft" || result.Phases[0].Status != zamnius.PhaseSucceeded {
		t.Errorf("first phase = %+v, want draft succeeded", result.Phases[0])
	}
	if result.RunID == [16]byte{} {
		t.Error("RunID is empty; a caller cannot inspect the run")
	}

	// The observer contract: facts arrive in execution order.
	if started := observer.Started(); len(started) != 2 || started[0] != "draft" {
		t.Errorf("observer started = %v, want [draft review]", started)
	}

	// Status is computed on demand and must agree with the result.
	report, err := eng.Status(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if report.Status != zamnius.RunSucceeded {
		t.Errorf("status report = %q, want succeeded", report.Status)
	}
	if report.NextAction == "" {
		t.Error("status report carries no next action")
	}
	if len(report.Phases) != 2 || report.Spent.Attempts != 2 {
		t.Errorf("status report = %+v, want two phases and two attempts of spend", report)
	}

	artifacts, err := eng.Artifacts(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Artifacts() error = %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2 (one per declared output)", len(artifacts))
	}
	for _, ref := range artifacts {
		if ref.SHA256 == "" || ref.Kind == "" || ref.SizeBytes <= 0 {
			t.Errorf("artifact %+v is incomplete", ref)
		}
	}

	verified, err := eng.Verify(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verified.OK() {
		t.Errorf("verification problems = %+v", verified.Problems)
	}
	if verified.Events == 0 || verified.Artifacts != 2 {
		t.Errorf("verification = %+v, want the events and both artifacts counted", verified)
	}
}

func TestFailedRunStillReturnsItsIdentifiers(t *testing.T) {
	t.Parallel()

	eng := adoptable(t, func(domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{}, &domain.Error{Class: domain.ErrClassAuth, Msg: "key rejected"}
	})

	def, err := definition.NewBuilder("failing").
		WithPhase("only", definition.Agent("a", "do", "gpt-4.1-mini", 256), definition.OnFailure(definition.FailAbort)).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}

	result, err := eng.Start(context.Background(), def, zamnius.StartOptions{})
	if err == nil {
		t.Fatal("Start() returned no error for an aborting failure")
	}
	if result.RunID == [16]byte{} {
		t.Error("a failed run must still report its RunID")
	}
	if result.Status != zamnius.RunFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if result.Err == nil {
		t.Error("RunResult.Err is nil; the caller has only the message")
	}
	if zamnius.ClassOf(err) != zamnius.ErrClassAuth {
		t.Errorf("class = %q, want auth", zamnius.ClassOf(err))
	}

	// The failed run is inspectable through the same public surface.
	report, err := eng.Status(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if report.Status != zamnius.RunFailed {
		t.Errorf("status report = %q, want failed", report.Status)
	}
}

func TestUnknownProviderIsActionable(t *testing.T) {
	t.Parallel()

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

	// The registry knows a provider the definition does not name: the failure must
	// be a classified permanent error naming the known providers, not a nil
	// dereference inside the phase.
	engine, err := zamnius.New(zamnius.Options{
		Store:     store,
		Artifacts: artifacts,
		Models:    registry{provider: "some-other-provider"},
		IDSource:  fakes.NewIDSource(),
	})
	if err != nil {
		t.Fatalf("zamnius.New() error = %v", err)
	}

	def, err := definition.NewBuilder("unknown-provider").
		WithPhase("one", definition.Agent("a", "do", "gpt-4.1-mini", 256)).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}

	result, err := engine.Start(context.Background(), def, zamnius.StartOptions{})
	if err == nil {
		t.Fatal("Start() resolved a provider no registry knows")
	}
	if result.Status != zamnius.RunFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if zamnius.ClassOf(err) != zamnius.ErrClassPermanent {
		t.Errorf("class = %q, want permanent", zamnius.ClassOf(err))
	}
	if hint := zamnius.HintOf(err); !strings.Contains(hint, "some-other-provider") {
		t.Errorf("hint %q does not name the known provider", hint)
	}
}
