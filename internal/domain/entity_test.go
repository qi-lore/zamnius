package domain_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

func TestConsumptionAddAndTokens(t *testing.T) {
	t.Parallel()

	a := domain.Consumption{Attempts: 1, WallClockMS: 10, TokensIn: 100, TokensOut: 50}
	b := domain.Consumption{Attempts: 2, WallClockMS: 5, TokensIn: 7, TokensOut: 3}

	got := a.Add(b)
	want := domain.Consumption{Attempts: 3, WallClockMS: 15, TokensIn: 107, TokensOut: 53}
	if got != want {
		t.Fatalf("Add() = %+v, want %+v", got, want)
	}
	if got.Tokens() != 160 {
		t.Errorf("Tokens() = %d, want 160", got.Tokens())
	}
	if a.Attempts != 1 {
		t.Errorf("Add mutated its receiver: %+v", a)
	}
}

func TestParseDurationStrictSingleUnit(t *testing.T) {
	t.Parallel()

	valid := map[string]time.Duration{
		"500ms": 500 * time.Millisecond,
		"30s":   30 * time.Second,
		"5m":    5 * time.Minute,
		"2h":    2 * time.Hour,
	}
	for text, want := range valid {
		got, err := domain.ParseDuration(text)
		if err != nil {
			t.Fatalf("ParseDuration(%q) error = %v", text, err)
		}
		if got.Duration() != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", text, got.Duration(), want)
		}
	}

	// Two units, a bare number, a bare unit, a negative value, and a fractional
	// value are all outside the restricted syntax.
	for _, text := range []string{"1m30s", "30", "s", "-5s", "1.5s", "1d", "", "0s"} {
		if _, err := domain.ParseDuration(text); err == nil {
			t.Errorf("ParseDuration(%q) accepted an out-of-syntax value", text)
		}
	}
}

func TestBudgetValidate(t *testing.T) {
	t.Parallel()

	zero, negative := 0, -1
	one, two := 1, 2
	dur := domain.NewDuration(30 * time.Second)

	tests := []struct {
		name    string
		budget  domain.Budget
		wantErr bool
	}{
		{"empty", domain.Budget{}, true},
		{"attempts only", domain.Budget{Attempts: &two}, false},
		{"wall clock only", domain.Budget{WallClock: &dur}, false},
		{"tokens only", domain.Budget{Tokens: &one}, false},
		{"attempts below one", domain.Budget{Attempts: &zero}, true},
		{"attempts negative", domain.Budget{Attempts: &negative}, true},
		{"tokens below one", domain.Budget{Tokens: &zero}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.budget.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClassifiedErrorReachesSentinels(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("acquire lease for run 42: %w", domain.ErrRunAlreadyOwned)
	if !errors.Is(wrapped, domain.ErrRunAlreadyOwned) {
		t.Fatal("errors.Is did not reach the sentinel through fmt.Errorf")
	}
	if got := domain.ClassOf(wrapped); got != domain.ErrClassPermanent {
		t.Errorf("ClassOf(wrapped) = %q, want permanent", got)
	}
	if got := domain.ClassOf(errors.New("socket closed")); got != domain.ErrClassInternal {
		t.Errorf("ClassOf(unclassified) = %q, want internal", got)
	}
	if got := domain.ClassOf(nil); got != "" {
		t.Errorf("ClassOf(nil) = %q, want empty", got)
	}

	configErr := &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "configuration is invalid",
		Hint:  `setting "log_format" accepts text, json; got "yaml"`,
	}
	if got := domain.HintOf(fmt.Errorf("load config: %w", configErr)); got == "" {
		t.Fatal("HintOf lost the operator hint")
	}
	if got := domain.HintOf(errors.New("no hint")); got != "" {
		t.Errorf("HintOf(plain error) = %q, want empty", got)
	}
}

func TestRunTerminalInvariant(t *testing.T) {
	t.Parallel()

	// data-model.md §2 invariant 3: a terminal run never transitions again.
	// The predicate is the single source of truth for that rule.
	for _, status := range []domain.RunStatus{domain.RunSucceeded, domain.RunFailed, domain.RunCancelled} {
		if !status.Terminal() {
			t.Errorf("%q must be terminal", status)
		}
	}
	for _, status := range []domain.RunStatus{domain.RunPending, domain.RunRunning, domain.RunPaused} {
		if status.Terminal() {
			t.Errorf("%q must not be terminal", status)
		}
	}
}
