package domain_test

import (
	"testing"

	"github.com/prompt-fu/zamnius/internal/domain"
)

func TestRunStatusValidAndTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   domain.RunStatus
		valid    bool
		terminal bool
	}{
		{domain.RunPending, true, false},
		{domain.RunRunning, true, false},
		{domain.RunPaused, true, false},
		{domain.RunSucceeded, true, true},
		{domain.RunFailed, true, true},
		{domain.RunCancelled, true, true},
		{"", false, false},
		{"done", false, false},
		{"SUCCEEDED", false, false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.valid {
			t.Errorf("RunStatus(%q).Valid() = %v, want %v", tt.status, got, tt.valid)
		}
		if got := tt.status.Terminal(); got != tt.terminal {
			t.Errorf("RunStatus(%q).Terminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestPhaseStatusValidAndTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   domain.PhaseStatus
		valid    bool
		terminal bool
	}{
		{domain.PhasePending, true, false},
		{domain.PhaseRunning, true, false},
		{domain.PhaseSucceeded, true, true},
		{domain.PhaseFailed, true, true},
		{domain.PhaseDegraded, true, true},
		{domain.PhaseCancelled, true, true},
		{domain.PhaseSkipped, true, true},
		{"partial", false, false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.valid {
			t.Errorf("PhaseStatus(%q).Valid() = %v, want %v", tt.status, got, tt.valid)
		}
		if got := tt.status.Terminal(); got != tt.terminal {
			t.Errorf("PhaseStatus(%q).Terminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestAttemptStatusTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   domain.AttemptStatus
		valid    bool
		terminal bool
	}{
		{domain.AttemptRunning, true, false},
		{domain.AttemptSucceeded, true, true},
		{domain.AttemptFailed, true, true},
		{domain.AttemptAbandoned, true, true},
		{"pending", false, false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.valid {
			t.Errorf("AttemptStatus(%q).Valid() = %v, want %v", tt.status, got, tt.valid)
		}
		if got := tt.status.Terminal(); got != tt.terminal {
			t.Errorf("AttemptStatus(%q).Terminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestErrorClassSet(t *testing.T) {
	t.Parallel()

	// The set is closed: every class named in contracts/model-client.md §4 exists,
	// and nothing else does. A new class must be added deliberately.
	want := map[domain.ErrorClass]bool{
		domain.ErrClassTransient:       true,
		domain.ErrClassRateLimited:     true,
		domain.ErrClassAuth:            true,
		domain.ErrClassPermanent:       true,
		domain.ErrClassContextOverflow: true,
		domain.ErrClassInvalidResponse: true,
		domain.ErrClassTimeout:         true,
		domain.ErrClassBudgetExhausted: true,
		domain.ErrClassCancelled:       true,
		domain.ErrClassInternal:        true,
	}
	for class := range want {
		if !class.Valid() {
			t.Errorf("ErrorClass(%q).Valid() = false, want true", class)
		}
	}
	if domain.ErrorClass("unknown_class").Valid() {
		t.Error("unrecognised error class reported as valid")
	}
	if !domain.ErrClassTransient.DefaultRetryable() ||
		!domain.ErrClassRateLimited.DefaultRetryable() ||
		!domain.ErrClassTimeout.DefaultRetryable() {
		t.Error("transport-class errors must be retryable by default")
	}
	if domain.ErrClassAuth.DefaultRetryable() || domain.ErrClassPermanent.DefaultRetryable() {
		t.Error("auth and permanent errors must not be retryable by default")
	}
}

func TestDecisionKindSet(t *testing.T) {
	t.Parallel()

	for _, kind := range []domain.DecisionKind{
		domain.DecisionRetry, domain.DecisionJoin, domain.DecisionFailurePolicy,
		domain.DecisionBudgetStop, domain.DecisionTakeover, domain.DecisionSkip,
		domain.DecisionResumeRestart,
	} {
		if !kind.Valid() {
			t.Errorf("DecisionKind(%q).Valid() = false, want true", kind)
		}
	}
	if domain.DecisionKind("guess").Valid() {
		t.Error("unrecognised decision kind reported as valid")
	}
}

func TestEventTypeVocabulary(t *testing.T) {
	t.Parallel()

	for _, event := range []domain.EventType{
		domain.EventRunCreated, domain.EventRunStarted, domain.EventRunPaused,
		domain.EventRunResumed, domain.EventRunTakenOver, domain.EventRunCancelRequested,
		domain.EventRunSucceeded, domain.EventRunFailed, domain.EventRunCancelled,
		domain.EventPhaseStarted, domain.EventPhaseSucceeded, domain.EventPhaseFailed,
		domain.EventPhaseDegraded, domain.EventPhaseSkipped, domain.EventPhaseBudgetExhausted,
		domain.EventAttemptStarted, domain.EventAttemptSucceeded, domain.EventAttemptFailed,
		domain.EventAttemptAbandoned, domain.EventArtifactCommitted, domain.EventArtifactVerified,
		domain.EventDecisionRecorded, domain.EventStateMigrated, domain.EventStateMigrationRefused,
	} {
		if !event.Valid() {
			t.Errorf("EventType(%q).Valid() = false, want true", event)
		}
	}
	if domain.EventType("phase.exploded").Valid() {
		t.Error("event outside the vocabulary reported as valid")
	}
}

func TestTerminalEventMapping(t *testing.T) {
	t.Parallel()

	if got := domain.RunTerminalEvent(domain.RunSucceeded); got != domain.EventRunSucceeded {
		t.Errorf("RunTerminalEvent(succeeded) = %q", got)
	}
	if got := domain.RunTerminalEvent(domain.RunPaused); got != "" {
		t.Errorf("RunTerminalEvent(paused) = %q, want empty", got)
	}
	if got := domain.PhaseTerminalEvent(domain.PhaseDegraded); got != domain.EventPhaseDegraded {
		t.Errorf("PhaseTerminalEvent(degraded) = %q", got)
	}
	if got := domain.PhaseTerminalEvent(domain.PhaseRunning); got != "" {
		t.Errorf("PhaseTerminalEvent(running) = %q, want empty", got)
	}
}
