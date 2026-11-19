package domain_test

import (
	"testing"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
)

// TestExecutionDefaults pins the values both surfaces default to. A change here is
// a behaviour change for every run that does not override it, so it must be a
// deliberate, reviewed diff rather than a quiet edit (ADR-0010).
func TestExecutionDefaults(t *testing.T) {
	t.Parallel()

	if domain.DefaultMaxConcurrency != 8 {
		t.Errorf("DefaultMaxConcurrency = %d, want 8", domain.DefaultMaxConcurrency)
	}
	if domain.DefaultLeaseTTL != 30*time.Second {
		t.Errorf("DefaultLeaseTTL = %s, want 30s", domain.DefaultLeaseTTL)
	}
}
