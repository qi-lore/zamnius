package recoverability_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/engine"
	"github.com/prompt-fu/zamnius/test/fakes"
)

// This file covers the lease heartbeat (T048, ADR-0004, FR-016). Ownership is a
// lease with a TTL, so a long phase must renew it while it runs — not only at
// phase boundaries — and a renewal that fails must demote the run to paused with
// the cause recorded rather than let the owner keep writing as a second owner.

// renewSpy wraps the durable store to observe and optionally fail lease renewals.
// It is the seam that makes the heartbeat's cadence and its failure path
// deterministic without changing production wiring.
type renewSpy struct {
	engine.Store
	mu     sync.Mutex
	renews int
	fail   error
}

func (s *renewSpy) RenewLease(ctx context.Context, l domain.Lease, ttl time.Duration) (domain.Lease, error) {
	s.mu.Lock()
	s.renews++
	fail := s.fail
	s.mu.Unlock()

	if fail != nil {
		return domain.Lease{}, fail
	}
	return s.Store.RenewLease(ctx, l, ttl)
}

func (s *renewSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renews
}

// slowModel answers after a delay, respecting cancellation. A phase that spends
// longer than the renewal interval inside one model call is exactly the case a
// phase-boundary renewal cannot cover.
type slowModel struct {
	delay time.Duration
}

func (m *slowModel) Complete(ctx context.Context, _ domain.ModelRequest) (domain.ModelResponse, error) {
	timer := time.NewTimer(m.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return domain.ModelResponse{}, ctx.Err()
	case <-timer.C:
	}
	return domain.ModelResponse{
		Content:    "done",
		Usage:      domain.Usage{InputTokens: 1, OutputTokens: 1},
		StopReason: domain.StopEnd,
	}, nil
}

// blockingModel blocks until its context is cancelled, which is how a heartbeat
// failure reaches the in-flight attempt.
type blockingModel struct {
	started chan struct{}
	once    sync.Once
}

func (m *blockingModel) Complete(ctx context.Context, _ domain.ModelRequest) (domain.ModelResponse, error) {
	m.once.Do(func() { close(m.started) })
	<-ctx.Done()
	return domain.ModelResponse{}, ctx.Err()
}

// singlePhaseDefinition is one phase, so the only renewal opportunity during its
// execution is the heartbeat.
func singlePhaseDefinition(t *testing.T) definition.Definition {
	t.Helper()

	def, err := definition.NewBuilder("heartbeat").
		WithPhase("work", definition.Agent("work", "do the work", "gpt-4.1-mini", 128),
			definition.WithOutputs("report")).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}
	return def
}

// TestLeaseHeartbeatRenewsDuringLongPhase proves renewal happens while a phase is
// running, not only between phases: one phase spends several renewal intervals in
// a single model call, and the lease is renewed more than once during it.
func TestLeaseHeartbeatRenewsDuringLongPhase(t *testing.T) {
	dir := t.TempDir()
	spy := &renewSpy{}
	f, err := newInstanceWith(dir, "owner-heartbeat", &slowModel{delay: 100 * time.Millisecond}, fakes.NewIDSource(),
		func(store engine.Store) engine.Store { spy.Store = store; return spy })
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = f.store.Close() })

	result, err := f.engine.Start(context.Background(), singlePhaseDefinition(t), engine.StartOptions{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	// The phase is one model call longer than the renewal interval, so any renewal
	// observed here came from the heartbeat, not a phase boundary.
	if got := spy.count(); got < 2 {
		t.Errorf("lease renewed %d times during a %s phase with a %s TTL, want at least 2: the heartbeat must renew while a phase runs",
			got, 100*time.Millisecond, leaseTTL)
	}
}

// TestLeaseRenewalFailurePausesRun proves a failed renewal demotes the run to
// paused with the cause recorded, instead of letting the owner keep writing after
// it has lost ownership (ADR-0004, FR-016).
func TestLeaseRenewalFailurePausesRun(t *testing.T) {
	dir := t.TempDir()
	spy := &renewSpy{fail: domain.ErrRunAlreadyOwned}
	model := &blockingModel{started: make(chan struct{})}
	f, err := newInstanceWith(dir, "owner-lost", model, fakes.NewIDSource(),
		func(store engine.Store) engine.Store { spy.Store = store; return spy })
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = f.store.Close() })

	// Start blocks until the run pauses, which the heartbeat triggers. The timeout
	// keeps a missing heartbeat a fast failure rather than a hung suite.
	type outcome struct {
		result engine.RunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.engine.Start(context.Background(), singlePhaseDefinition(t), engine.StartOptions{})
		done <- outcome{result: result, err: err}
	}()

	var result engine.RunResult
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("Start() error = nil, want a pause error after the lease was lost")
		}
		result = got.result
	case <-time.After(3 * time.Second):
		t.Fatal("Start() did not return: a failed lease renewal must demote the run to paused, not let it keep running")
	}
	if result.Status != domain.RunPaused {
		t.Fatalf("status = %q, want paused", result.Status)
	}

	// The durable record must agree: the run is paused, not running or succeeded.
	run, loadErr := f.store.LoadRun(context.Background(), result.RunID)
	if loadErr != nil {
		t.Fatalf("LoadRun() error = %v", loadErr)
	}
	if run.Status != domain.RunPaused {
		t.Errorf("stored status = %q, want paused", run.Status)
	}

	// The pause reason must name the lease, so an operator can tell this apart from
	// a caller cancellation.
	reason := pauseReason(t, f, result.RunID)
	if !strings.Contains(strings.ToLower(reason), "lease") {
		t.Errorf("run.paused reason = %q, want it to name the lease renewal failure", reason)
	}
}

// pauseReason returns the reason recorded on the run's run.paused event.
func pauseReason(t *testing.T, f *fixture, runID uuid.UUID) string {
	t.Helper()

	events, err := f.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	for _, event := range events {
		if event.Type != string(domain.EventRunPaused) {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode run.paused payload: %v", err)
		}
		if reason, ok := payload["reason"].(string); ok {
			return reason
		}
	}
	t.Fatal("no run.paused event was recorded")
	return ""
}
