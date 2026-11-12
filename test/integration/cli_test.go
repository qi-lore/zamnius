package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hIMEI29A/zamnius/internal/app"
	"github.com/hIMEI29A/zamnius/internal/cli"
	"github.com/hIMEI29A/zamnius/internal/observability"
)

// cliEnv is one isolated command environment: a state directory and a fake
// OpenAI-compatible provider, so a CLI test never needs a live key or a network.
type cliEnv struct {
	t        *testing.T
	stateDir string
}

// newCLIEnv wires the environment the binary would inherit: the state directory,
// the provider root, and a canary credential the redactor must keep out of every
// artifact, event, and log line.
func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"choices":[{"message":{"role":"assistant","content":"answer %d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`,
			calls.Add(1))
	}))
	t.Cleanup(server.Close)

	t.Setenv(app.BaseURLEnvVar, server.URL)
	t.Setenv(observability.SecretEnvVar, "sk-test-canary")
	t.Setenv("ZAMNIUS_LOG_LEVEL", "error")

	return &cliEnv{t: t, stateDir: filepath.Join(t.TempDir(), "state")}
}

// fixture resolves a workflow fixture from the test data directory.
func fixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "testdata", "workflows", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s is missing: %v", path, err)
	}
	return path
}

// run executes one command in-process, which is how the CLI is tested: the binary
// itself only binds a context and an exit code (T041).
func (e *cliEnv) run(args ...string) (int, string, string) {
	e.t.Helper()

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// runJSON executes a command with --json and decodes the single envelope object.
func (e *cliEnv) runJSON(args ...string) (int, map[string]any, string) {
	e.t.Helper()

	code, stdout, stderr := e.run(append(args, "--json")...)
	if strings.TrimSpace(stdout) == "" {
		return code, nil, stderr
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		e.t.Fatalf("--json output is not one JSON object: %v\noutput: %s", err, stdout)
	}
	return code, envelope, stderr
}

// runID extracts the run identifier from a start envelope or a human line.
func runID(t *testing.T, envelope map[string]any) string {
	t.Helper()

	run, ok := envelope["run"].(map[string]any)
	if !ok {
		t.Fatalf("envelope has no run object: %v", envelope)
	}
	id, _ := run["id"].(string)
	if id == "" {
		t.Fatalf("run object has no id: %v", run)
	}
	return id
}

// artifactsOf returns the envelope's artifact list.
func artifactsOf(t *testing.T, envelope map[string]any) []map[string]any {
	t.Helper()

	raw, ok := envelope["artifacts"].([]any)
	if !ok {
		t.Fatalf("envelope has no artifacts array: %v", envelope)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("artifact entry is not an object: %v", entry)
		}
		out = append(out, item)
	}
	return out
}

func TestCLIStartStatusAndArtifactsShareOneEnvelope(t *testing.T) {
	env := newCLIEnv(t)

	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "hello.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	// The envelope contract: exactly these keys, and the versioned shape.
	if got := envelope["api_version"]; got != "1" {
		t.Errorf("api_version = %v, want \"1\"", got)
	}
	if got := envelope["command"]; got != "run.start" {
		t.Errorf("command = %v, want run.start", got)
	}
	if got := envelope["ok"]; got != true {
		t.Errorf("ok = %v, want true", got)
	}
	if got, present := envelope["error"]; present && got != nil {
		t.Errorf("error = %v, want null on success", got)
	}
	for _, key := range []string{"api_version", "command", "ok", "run", "artifacts", "error"} {
		if _, present := envelope[key]; !present {
			t.Errorf("envelope is missing the %q key", key)
		}
	}

	run, _ := envelope["run"].(map[string]any)
	if got := run["status"]; got != "succeeded" {
		t.Errorf("run.status = %v, want succeeded", got)
	}
	phases, _ := run["phases"].([]any)
	if len(phases) != 1 {
		t.Fatalf("run.phases = %v, want one phase", run["phases"])
	}
	phase, _ := phases[0].(map[string]any)
	if got := phase["status"]; got != "succeeded" {
		t.Errorf("phase status = %v, want succeeded", got)
	}
	if got, ok := phase["attempts"].(float64); !ok || got != 1 {
		t.Errorf("phase attempts = %v, want 1", phase["attempts"])
	}

	artifacts := artifactsOf(t, envelope)
	if len(artifacts) == 0 {
		t.Fatal("no artifacts reported for a succeeded phase that declares outputs")
	}
	for _, artifact := range artifacts {
		for _, key := range []string{"sha256", "kind", "media_type", "size_bytes", "phase"} {
			if _, present := artifact[key]; !present {
				t.Errorf("artifact %v is missing %q", artifact, key)
			}
		}
		sha, _ := artifact["sha256"].(string)
		if len(sha) != 64 {
			t.Errorf("artifact sha256 = %q, want a hex digest", sha)
		}
		if size, _ := artifact["size_bytes"].(float64); size <= 0 {
			t.Errorf("artifact size_bytes = %v, want a positive size", artifact["size_bytes"])
		}
	}

	id := runID(t, envelope)

	// Status reports the same run without needing the credential to execute work.
	code, statusEnvelope, stderr := env.runJSON("run", "status", id, "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run status exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if got := statusEnvelope["command"]; got != "run.status" {
		t.Errorf("command = %v, want run.status", got)
	}
	statusRun, _ := statusEnvelope["run"].(map[string]any)
	if got := statusRun["status"]; got != "succeeded" {
		t.Errorf("status run.status = %v, want succeeded", got)
	}
	if got, ok := statusRun["attempts"].(float64); !ok || got != 1 {
		t.Errorf("status attempts = %v, want 1", statusRun["attempts"])
	}
	if got, ok := statusRun["spent"].(map[string]any); !ok || got["tokens_in"] != float64(12) {
		t.Errorf("status spent = %v, want the recorded 12 input tokens", statusRun["spent"])
	}

	// Artifacts lists exactly what start reported, keyed by the same digests.
	code, artifactEnvelope, stderr := env.runJSON("run", "artifacts", id, "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run artifacts exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if got := artifactEnvelope["command"]; got != "run.artifacts" {
		t.Errorf("command = %v, want run.artifacts", got)
	}
	listed := artifactsOf(t, artifactEnvelope)
	if len(listed) != len(artifacts) {
		t.Errorf("listed artifacts = %d, want the %d reported by start", len(listed), len(artifacts))
	}
	want := map[string]bool{}
	for _, artifact := range artifacts {
		want[artifact["sha256"].(string)] = true
	}
	for _, artifact := range listed {
		sha, _ := artifact["sha256"].(string)
		if !want[sha] {
			t.Errorf("listed artifact %s was not reported by start", sha)
		}
	}
}

func TestCLIHumanOutputEndsWithTheCitableLine(t *testing.T) {
	env := newCLIEnv(t)

	code, stdout, stderr := env.run("run", "start", "--file", fixture(t, "hello.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "run ") || !strings.Contains(last, "finished: succeeded (") {
		t.Fatalf("last line = %q, want a citable \"run <id> finished: succeeded (...)\" line", last)
	}
	if !strings.Contains(last, "1 phase") || !strings.Contains(last, "1 attempt") || !strings.Contains(last, "artifact") {
		t.Errorf("summary line = %q, want phase, attempt, and artifact counts", last)
	}

	// Status without --json is prose, and it must not be the JSON envelope.
	code, stdout, _ = env.run("run", "status", strings.Fields(last)[1], "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run status exit = %d, want 0", code)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("human status output looks like JSON: %q", stdout)
	}
	if !strings.Contains(stdout, "succeeded") {
		t.Errorf("human status output %q does not name the status", stdout)
	}
}

func TestCLIReportsUsageErrorsAndVersion(t *testing.T) {
	env := newCLIEnv(t)

	code, _, stderr := env.run("run", "start")
	if code != 2 {
		t.Errorf("missing --file exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(stderr, "--file") {
		t.Errorf("usage message %q does not name the missing flag", stderr)
	}

	code, _, _ = env.run("run", "frobnicate")
	if code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}

	code, _, _ = env.run("nonsense")
	if code != 2 {
		t.Errorf("unknown command exit = %d, want 2", code)
	}

	code, stdout, stderr := env.run("version")
	if code != 0 {
		t.Fatalf("version exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, cli.Version) {
		t.Errorf("version output %q does not name the version %q", stdout, cli.Version)
	}
	if !strings.Contains(stdout, `"1"`) && !strings.Contains(stdout, "api_version") {
		t.Errorf("version output %q does not name the api version", stdout)
	}
}

func TestCLIInvalidDefinitionExitsFiveWithAFieldPath(t *testing.T) {
	env := newCLIEnv(t)

	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "invalid.yaml"), "--state-dir", env.stateDir)
	if code != 5 {
		t.Fatalf("invalid definition exit = %d, want 5 (stderr: %s)", code, stderr)
	}
	if got := envelope["ok"]; got != false {
		t.Errorf("ok = %v, want false", got)
	}
	if got := envelope["command"]; got != "run.start" {
		t.Errorf("command = %v, want run.start even on failure", got)
	}
	failure, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %v, want an object", envelope["error"])
	}
	hint, _ := failure["hint"].(string)
	if hint == "" {
		t.Error("error.hint is empty; configuration failures must say what to fix")
	}
	// The hint has to name the offending field path, otherwise the operator is
	// left guessing which phase or key is wrong (FR-052, SC-012).
	if !strings.Contains(hint, "phases") && !strings.Contains(hint, "phase") {
		t.Errorf("error.hint = %q, want it to name the offending field path", hint)
	}
	if got, _ := failure["class"].(string); got == "" {
		t.Error("error.class is empty")
	}

	// The same failure in human form goes to stderr and stays citable.
	code, stdout, stderr := env.run("run", "start", "--file", fixture(t, "invalid.yaml"), "--state-dir", env.stateDir)
	if code != 5 {
		t.Errorf("human invalid definition exit = %d, want 5", code)
	}
	if strings.Contains(stdout, "{") {
		t.Errorf("human failure wrote JSON to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "hint:") && !strings.Contains(stderr, "hint") {
		t.Errorf("human failure %q does not print the hint", stderr)
	}
}
