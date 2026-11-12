package fakes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

func TestModelClientReplaysScriptAndRecordsCalls(t *testing.T) {
	t.Parallel()

	client := fakes.NewModelClient(
		fakes.Text("first", 10, 5),
		fakes.Text("second", 1, 2),
	)
	ctx := context.Background()

	first, err := client.Complete(ctx, domain.ModelRequest{Model: "m", System: "sys"})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.Content != "first" || first.Usage.InputTokens != 10 || first.Usage.OutputTokens != 5 {
		t.Fatalf("response = %+v", first)
	}
	if first.StopReason != domain.StopEnd {
		t.Errorf("stop reason = %q, want stop", first.StopReason)
	}

	if _, err := client.Complete(ctx, domain.ModelRequest{Model: "m"}); err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
	if got := client.Remaining(); got != 0 {
		t.Errorf("Remaining() = %d, want 0", got)
	}
	calls := client.Calls()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(calls))
	}
	if calls[0].System != "sys" {
		t.Errorf("recorded system prompt = %q", calls[0].System)
	}
}

func TestModelClientExhaustionIsClassified(t *testing.T) {
	t.Parallel()

	client := fakes.NewModelClient()
	_, err := client.Complete(context.Background(), domain.ModelRequest{})
	if err == nil {
		t.Fatal("exhausted script returned no error")
	}
	if got := domain.ClassOf(err); got != domain.ErrClassInvalidResponse {
		t.Errorf("class = %q, want invalid_response", got)
	}
}

func TestModelClientHonoursCancellation(t *testing.T) {
	t.Parallel()

	client := fakes.NewModelClient(fakes.Step{
		Response: domain.ModelResponse{Content: "late", Usage: domain.Usage{InputTokens: 1}},
		Delay:    10 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Complete(ctx, domain.ModelRequest{})
	if got := domain.ClassOf(err); got != domain.ErrClassCancelled {
		t.Fatalf("class = %q, want cancelled (error %v)", got, err)
	}
}

func TestRegistryNamesKnownProviders(t *testing.T) {
	t.Parallel()

	registry := fakes.NewRegistry().Register("fake", fakes.NewModelClient())
	if _, err := registry.Client("fake"); err != nil {
		t.Fatalf("Client(fake) error = %v", err)
	}

	_, err := registry.Client("openai-compatible")
	if err == nil {
		t.Fatal("unknown provider resolved")
	}
	if hint := domain.HintOf(err); hint == "" {
		t.Fatal("error must name the known providers")
	}
}

func TestClockAdvancesAndIsOrdered(t *testing.T) {
	t.Parallel()

	clock := fakes.NewClock(time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), time.Second)
	first := clock.Now()
	second := clock.Now()
	if !second.After(first) {
		t.Fatalf("clock did not advance: %v then %v", first, second)
	}
	if got := clock.Peek(); got != second.Add(time.Second) {
		t.Errorf("Peek() = %v, want the next instant", got)
	}
}

func TestIDSourceIsSequentialAndValid(t *testing.T) {
	t.Parallel()

	ids := fakes.NewIDSource()
	first, second := ids(), ids()
	if first == second {
		t.Fatal("id source repeated itself")
	}
	if first == uuid.Nil || second == uuid.Nil {
		t.Fatal("id source produced a nil uuid")
	}
	if first.String() >= second.String() {
		t.Errorf("ids are not ordered: %s then %s", first, second)
	}
}

func TestObserverRecordsInOrder(t *testing.T) {
	t.Parallel()

	observer := fakes.NewObserver()
	ctx := context.Background()
	runID := uuid.New()

	observer.OnPhaseStarted(ctx, runID, "plan")
	observer.OnAttemptFinished(ctx, runID, "plan", 1, domain.ErrClassTransient)
	observer.OnPhaseFinished(ctx, runID, "plan", domain.PhaseSucceeded)

	if started := observer.Started(); len(started) != 1 || started[0] != "plan" {
		t.Errorf("Started() = %v", started)
	}
	attempts := observer.Attempts()
	if len(attempts) != 1 || attempts[0].Attempt != 1 || attempts[0].Class != domain.ErrClassTransient {
		t.Errorf("Attempts() = %+v", attempts)
	}
	finished := observer.Finished()
	if len(finished) != 1 || finished[0].Status != domain.PhaseSucceeded {
		t.Errorf("Finished() = %+v", finished)
	}
}

func TestErrorIsClassified(t *testing.T) {
	t.Parallel()

	err := fakes.Error(domain.ErrClassRateLimited, "429")
	if !errors.Is(err, err) || domain.ClassOf(err) != domain.ErrClassRateLimited {
		t.Fatalf("error = %v", err)
	}
}

func TestTempStateDirIsIsolatedAndStructured(t *testing.T) {
	t.Parallel()

	first := fakes.TempStateDir(t)
	second := fakes.TempStateDir(t)
	if first == second {
		t.Fatal("TempStateDir reused a directory")
	}

	info, err := os.Stat(filepath.Join(first, "artifacts"))
	if err != nil || !info.IsDir() {
		t.Fatalf("artifacts subdirectory missing: %v", err)
	}
	layout := fakes.StateLayout(first)
	if layout.Database != filepath.Join(first, "zamnius.db") {
		t.Errorf("database path = %q", layout.Database)
	}
	if layout.Artifact != filepath.Join(first, "artifacts") {
		t.Errorf("artifact path = %q", layout.Artifact)
	}
}
