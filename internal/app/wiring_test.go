package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qi-lore/zamnius/internal/config"
)

// TestEngineOptionsCarryTheResolvedSettings is the test for ADR-0010's config
// surface: a value an operator set must reach the engine, not sit in a struct the
// composition root ignores. It asserts the mapping itself — `Engine()` needs a
// reachable provider and a credential, and neither is required to prove the
// translation.
func TestEngineOptionsCarryTheResolvedSettings(t *testing.T) {
	t.Parallel()

	settings := config.Settings{
		StateDir:       filepath.Join(t.TempDir(), "state"),
		LogLevel:       config.DefaultLogLevel,
		LogFormat:      config.DefaultLogFormat,
		MaxConcurrency: config.DefaultMaxConcurrency,
		LeaseTTL:       90 * time.Second,
	}

	composition, err := Open(context.Background(), settings)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = composition.Close() })

	opts := composition.engineOptions(nil)
	if opts.LeaseTTL != 90*time.Second {
		t.Errorf("engine lease TTL = %s, want the resolved setting 90s", opts.LeaseTTL)
	}
	if opts.Store == nil || opts.Artifacts == nil {
		t.Error("engine options lost a store: the composition root must pass both")
	}
}
