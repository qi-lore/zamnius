package app_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prompt-fu/zamnius/internal/app"
	"github.com/prompt-fu/zamnius/internal/config"
	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/observability"
	"github.com/prompt-fu/zamnius/pkg/zamnius"
)

// provider is a stand-in OpenAI-compatible service. It answers every request with
// a well-formed completion whose content differs per call, so the composition root
// can be exercised end to end without a network and every produced artifact is a
// distinct document rather than a deduplicated one.
func provider(t *testing.T) *httptest.Server {
	t.Helper()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("provider path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("provider received no Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"choices":[{"message":{"role":"assistant","content":"composed output %d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
			calls.Add(1))
	}))
	t.Cleanup(server.Close)
	return server
}

// settings resolves configuration the way the process does: an explicit state
// directory plus the environment.
func settings(t *testing.T, stateDir string, environ ...string) config.Settings {
	t.Helper()

	resolved, err := config.Load(context.Background(), config.Sources{
		Overrides: config.Overrides{StateDir: &stateDir},
		Environ:   environ,
		// Discovery is pointed at an empty directory so the test cannot pick up a
		// configuration file from the operator's working directory.
		DiscoverDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return resolved
}

func TestCompositionRootRunsAWorkflowEndToEnd(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	server := provider(t)
	t.Setenv(app.BaseURLEnvVar, server.URL)
	t.Setenv(observability.SecretEnvVar, "canary-key-never-real")

	resolved := settings(t, stateDir, "ZAMNIUS_LOG_LEVEL=error", "ZAMNIUS_MAX_CONCURRENCY=3")
	if resolved.StateDir != stateDir {
		t.Fatalf("state dir = %q, want the override %q", resolved.StateDir, stateDir)
	}
	if resolved.MaxConcurrency != 3 {
		t.Errorf("max concurrency = %d, want 3", resolved.MaxConcurrency)
	}

	ctx := context.Background()
	composition, err := app.Open(ctx, resolved)
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = composition.Close() })

	// The layout an operator (or a recovery run) depends on.
	if _, err := os.Stat(composition.Paths.Database); err != nil {
		t.Errorf("state database missing: %v", err)
	}
	if info, err := os.Stat(composition.Paths.Artifacts); err != nil || !info.IsDir() {
		t.Errorf("artifact root missing: %v", err)
	}
	if info, err := os.Stat(composition.Paths.Dir); err != nil {
		t.Fatalf("state directory missing: %v", err)
	} else if info.Mode().Perm() != 0o750 {
		t.Errorf("state directory mode = %o, want 750 (the audit trail is owner-only)", info.Mode().Perm())
	}
	if composition.Paths.Database != filepath.Join(stateDir, "zamnius.db") {
		t.Errorf("database path = %q, want %q", composition.Paths.Database, filepath.Join(stateDir, "zamnius.db"))
	}

	engine, err := composition.Engine()
	if err != nil {
		t.Fatalf("app.Engine() error = %v", err)
	}

	def, err := definition.NewBuilder("composed").
		WithPhase("draft", definition.Agent("writer", "write", "gpt-4.1-mini", 128), definition.WithOutputs("plan")).
		WithFanout("review",
			definition.JoinPolicy{Rule: definition.JoinAll},
			[]definition.Branch{
				definition.BranchOf("alpha", definition.Agent("alpha", "review", "gpt-4.1-mini", 128)),
				definition.BranchOf("beta", definition.Agent("beta", "review", "gpt-4.1-mini", 128)),
			},
			definition.WithOutputs("aggregate")).
		Build()
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}

	result, err := engine.Start(ctx, def, zamnius.StartOptions{MaxConcurrency: resolved.MaxConcurrency})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Status != zamnius.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	report, err := engine.Status(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if report.Spent.Attempts != 3 || report.Spent.TokensIn != 27 {
		t.Errorf("recorded spend = %+v, want 3 attempts and 27 input tokens (three calls of 9)", report.Spent)
	}

	verified, err := engine.Verify(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verified.OK() {
		t.Fatalf("verification problems = %+v", verified.Problems)
	}
	// Four rows: the plan, one per branch, and the aggregate. Content is addressed
	// by hash, so identical branch output would legitimately collapse to one row;
	// the provider above makes each answer distinct to keep this assertion sharp.
	if verified.Artifacts != 4 {
		t.Errorf("verified artifacts = %d, want 4 (plan, two branch outputs, aggregate)", verified.Artifacts)
	}
}

func TestInspectionWorksWithoutAModelCredential(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv(app.BaseURLEnvVar, "")
	t.Setenv(observability.SecretEnvVar, "")

	ctx := context.Background()
	composition, err := app.Open(ctx, settings(t, stateDir, "ZAMNIUS_LOG_LEVEL=error"))
	if err != nil {
		t.Fatalf("app.Open() error = %v; opening a state directory must not need a credential", err)
	}
	t.Cleanup(func() { _ = composition.Close() })

	engine, err := composition.Engine()
	if err == nil {
		t.Fatal("app.Engine() built an engine with no API key")
	}
	if engine != nil {
		t.Error("app.Engine() returned an engine alongside the error")
	}
	if !errors.Is(err, domain.ErrInvalidConfig) {
		t.Errorf("error %v is not an invalid-configuration failure", err)
	}
	hint := domain.HintOf(err)
	if !strings.Contains(hint, observability.SecretEnvVar) {
		t.Errorf("hint %q does not name %s", hint, observability.SecretEnvVar)
	}
}

func TestOpenRefusesAnUnwritableStateDirectory(t *testing.T) {
	// A file where the state directory should be is the portable way to make
	// MkdirAll fail without depending on unix permissions.
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocker: %v", err)
	}

	composition, err := app.Open(context.Background(), settings(t, filepath.Join(blocker, "state")))
	if err == nil {
		_ = composition.Close()
		t.Fatal("app.Open() accepted a state directory it cannot create")
	}
	if !errors.Is(err, domain.ErrInvalidConfig) {
		t.Errorf("error %v is not an invalid-configuration failure", err)
	}
	if domain.HintOf(err) == "" {
		t.Error("no hint telling the operator which setting to change")
	}
}
