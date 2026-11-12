package zamnius_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hIMEI29A/zamnius/pkg/zamnius"
)

func TestSentinelsMatchThroughWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		sentinel error
		class    zamnius.ErrorClass
	}{
		{"owned", zamnius.ErrRunAlreadyOwned, zamnius.ErrClassPermanent},
		{"stale", zamnius.ErrStaleRecord, zamnius.ErrClassInternal},
		{"integrity", zamnius.ErrIntegrity, zamnius.ErrClassInternal},
		{"definition", zamnius.ErrInvalidDefinition, zamnius.ErrClassPermanent},
		{"config", zamnius.ErrInvalidConfig, zamnius.ErrClassPermanent},
		{"terminal", zamnius.ErrTerminalRun, zamnius.ErrClassPermanent},
		{"drift", zamnius.ErrDefinitionDrift, zamnius.ErrClassPermanent},
		{"migration", zamnius.ErrMigrationRefused, zamnius.ErrClassPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("layer: %w", tc.sentinel)
			if !errors.Is(wrapped, tc.sentinel) {
				t.Fatal("errors.Is failed through wrapping")
			}
			if got := zamnius.ClassOf(wrapped); got != tc.class {
				t.Errorf("ClassOf = %q, want %q", got, tc.class)
			}
		})
	}
}

func TestHintIsReadableByCallers(t *testing.T) {
	t.Parallel()

	cause := &zamnius.Error{
		Class: zamnius.ErrClassPermanent,
		Msg:   "configuration is invalid",
		Hint:  `setting "log_level" accepts debug, info, warn, error; got "verbose"`,
	}
	err := fmt.Errorf("start: %w", cause)
	if got := zamnius.HintOf(err); got == "" {
		t.Fatal("hint was lost")
	}
	if got := zamnius.HintOf(errors.New("no hint")); got != "" {
		t.Errorf("HintOf(plain) = %q, want empty", got)
	}
}

func TestDurableTypesAreTheDomainTypes(t *testing.T) {
	t.Parallel()

	// The public aliases must be the same type the store persists; an accidental
	// copy would silently fork the contract.
	var run zamnius.Run
	run.Status = zamnius.RunRunning
	if run.Status.Valid() != true {
		t.Fatal("aliased status type lost its behaviour")
	}
	if zamnius.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", zamnius.SchemaVersion)
	}
}
