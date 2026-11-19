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

	"github.com/qi-lore/zamnius/internal/app"
	"github.com/qi-lore/zamnius/internal/cli"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/observability"
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

// lastEventsOf returns the run view's recent-events array.
func lastEventsOf(t *testing.T, run map[string]any) []map[string]any {
	t.Helper()

	raw, ok := run["last_events"].([]any)
	if !ok {
		t.Fatalf("run has no last_events array: %v", run)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("event entry is not an object: %v", entry)
		}
		out = append(out, item)
	}
	return out
}

// TestCLIStatusEnvelopeCarriesTheEnrichedView locks the enriched status surface
// (T061): the position the contract names, the next action, and the last five
// events — in the versioned envelope and in the human prose, from one derivation.
func TestCLIStatusEnvelopeCarriesTheEnrichedView(t *testing.T) {
	env := newCLIEnv(t)

	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "hello.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	id := runID(t, envelope)

	code, statusEnvelope, stderr := env.runJSON("run", "status", id, "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run status exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	run, _ := statusEnvelope["run"].(map[string]any)
	if run == nil {
		t.Fatalf("status envelope has no run object: %v", statusEnvelope)
	}

	if action, _ := run["next_action"].(string); action == "" {
		t.Errorf("run.next_action = %v, want the next expected action", run["next_action"])
	}

	events := lastEventsOf(t, run)
	if len(events) == 0 {
		t.Fatalf("run.last_events is empty, want the run's recent events")
	}
	if len(events) > 5 {
		t.Errorf("last_events carried %d entries, want at most the 5 the contract fixes", len(events))
	}
	sawTerminal := false
	for _, event := range events {
		for _, key := range []string{"seq", "type", "ts", "actor"} {
			if _, present := event[key]; !present {
				t.Errorf("event %v is missing %q", event, key)
			}
		}
		if _, ok := event["seq"].(float64); !ok {
			t.Errorf("event seq is not a number: %v", event["seq"])
		}
		if event["type"] == "run.succeeded" {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Errorf("last_events %v do not include the run's terminal event", events)
	}

	// The human form must print the same tail, not only the JSON envelope.
	code, stdout, _ := env.run("run", "status", id, "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("human run status exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "last events:") {
		t.Errorf("human status %q does not introduce the event tail", stdout)
	}
	if !strings.Contains(stdout, "run.succeeded") {
		t.Errorf("human status %q does not name the terminal event", stdout)
	}
}

// TestCLIArtifactsExportVerifiesEveryFingerprint proves the `--out` contract
// (T061, contracts/cli.md §1): every committed artifact is copied, and each copy
// hashes to the digest the record gives for it.
func TestCLIArtifactsExportVerifiesEveryFingerprint(t *testing.T) {
	env := newCLIEnv(t)

	// A fan-out run produces several artifacts (three branches, an aggregate, and a
	// summary), so more than one fingerprint is exercised.
	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "fanout.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	id := runID(t, envelope)
	artifacts := artifactsOf(t, envelope)
	if len(artifacts) < 2 {
		t.Fatalf("run produced %d artifacts, want several to verify the export", len(artifacts))
	}

	out := filepath.Join(t.TempDir(), "export")
	code, stdout, stderr := env.run("run", "artifacts", id, "--state-dir", env.stateDir, "--out", out)
	if code != 0 {
		t.Fatalf("run artifacts --out exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	for _, artifact := range artifacts {
		sha, _ := artifact["sha256"].(string)
		kind, _ := artifact["kind"].(string)
		content, err := os.ReadFile(filepath.Join(out, kind+"-"+sha))
		if err != nil {
			t.Errorf("exported artifact for %s: %v", sha, err)
			continue
		}
		if got := domain.HashBytes(content); got != sha {
			t.Errorf("exported %s hashes to %s, want %s", kind, got, sha)
		}
		if size, _ := artifact["size_bytes"].(float64); int64(len(content)) != int64(size) {
			t.Errorf("exported %s size = %d, want %d", kind, len(content), int64(size))
		}
	}
	if !strings.Contains(stdout, "exported") {
		t.Errorf("human export output %q does not confirm the export", stdout)
	}
}

// TestCLIArtifactsExportAbortsOnACorruptedFile is the tamper-evidence check
// (T061, contracts/cli.md §1, §3, quickstart §6): a stored artifact whose content
// no longer matches its fingerprint makes the export exit `6`, names the digest,
// and leaves no corrupt copy behind.
func TestCLIArtifactsExportAbortsOnACorruptedFile(t *testing.T) {
	env := newCLIEnv(t)

	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "hello.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	id := runID(t, envelope)
	artifacts := artifactsOf(t, envelope)
	if len(artifacts) == 0 {
		t.Fatal("run produced no artifacts to corrupt")
	}
	sha, _ := artifacts[0]["sha256"].(string)

	// Corrupt the stored content-addressed file, exactly as a bit-rot or a
	// tampering would, leaving the row and its expected digest untouched.
	casFile := filepath.Join(app.PathsFor(env.stateDir).Artifacts, sha[:2], sha)
	if err := os.WriteFile(casFile, []byte("tampered content"), 0o600); err != nil {
		t.Fatalf("corrupt the stored artifact %s: %v", casFile, err)
	}

	out := filepath.Join(t.TempDir(), "export")
	code, exportEnvelope, stderr := env.runJSON("run", "artifacts", id, "--state-dir", env.stateDir, "--out", out)
	if code != 6 {
		t.Fatalf("corrupted export exit = %d, want 6 (stderr: %s)", code, stderr)
	}
	if got := exportEnvelope["ok"]; got != false {
		t.Errorf("ok = %v, want false", got)
	}
	failure, ok := exportEnvelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %v, want an object", exportEnvelope["error"])
	}
	if message, _ := failure["message"].(string); !strings.Contains(message, sha) {
		t.Errorf("error.message = %q, want it to name the offending digest", message)
	}
	if hint, _ := failure["hint"].(string); !strings.Contains(hint, sha) {
		t.Errorf("error.hint = %q, want it to name the offending digest", hint)
	}
	// A mismatch must not leave a corrupt copy behind.
	if entries, err := os.ReadDir(out); err == nil && len(entries) != 0 {
		t.Errorf("a corrupted artifact was exported: %v", entries)
	}
}

// TestCLISurfaceMatchesTheDocumentedOperations locks the parity SC-011 asks for:
// every core operation the library documents has a command-line counterpart. The
// library's five operations are Start, Resume, Status, Artifacts, and Verify
// (contracts/library-api.md §Entry point), so the usage text must advertise all
// five. Verification was the missing one until this command existed.
func TestCLISurfaceMatchesTheDocumentedOperations(t *testing.T) {
	env := newCLIEnv(t)

	code, stdout, stderr := env.run("help")
	if code != 0 {
		t.Fatalf("help exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	for _, operation := range []string{"start", "resume", "status", "artifacts", "verify"} {
		if !strings.Contains(stdout, "run "+operation) {
			t.Errorf("the CLI does not advertise `run %s`, which the library documents", operation)
		}
	}
}

// TestCLIVerifyAcceptsACleanRunAndRefusesACorruptedOne proves the command the
// parity criterion needs: a finished run can be checked without resuming it. A
// clean record verifies with exit `0`; a tampered artifact file is exit `6`, and
// both the error and the structured verification section name the digest that
// disagrees (contracts/cli.md §1, §2, §3).
func TestCLIVerifyAcceptsACleanRunAndRefusesACorruptedOne(t *testing.T) {
	env := newCLIEnv(t)

	code, envelope, stderr := env.runJSON("run", "start", "--file", fixture(t, "hello.yaml"), "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	id := runID(t, envelope)

	// A clean run verifies: the command is named, the verification section carries
	// the counts, and no problem is reported.
	code, verifyEnvelope, stderr := env.runJSON("run", "verify", id, "--state-dir", env.stateDir)
	if code != 0 {
		t.Fatalf("run verify exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if got := verifyEnvelope["command"]; got != "run.verify" {
		t.Errorf("command = %v, want run.verify", got)
	}
	verification, ok := verifyEnvelope["verification"].(map[string]any)
	if !ok {
		t.Fatalf("envelope has no verification object: %v", verifyEnvelope)
	}
	if got, _ := verification["events"].(float64); got <= 0 {
		t.Errorf("verification.events = %v, want the run's event count", verification["events"])
	}
	if problems, present := verification["problems"]; present {
		t.Errorf("a clean run reported problems: %v", problems)
	}

	artifacts := artifactsOf(t, envelope)
	if len(artifacts) == 0 {
		t.Fatal("run produced no artifacts to corrupt")
	}
	sha, _ := artifacts[0]["sha256"].(string)

	// Corrupt the stored content-addressed file, exactly as the export test does:
	// the row and its recorded digest stay intact, so the disagreement is real.
	casFile := filepath.Join(app.PathsFor(env.stateDir).Artifacts, sha[:2], sha)
	if err := os.WriteFile(casFile, []byte("tampered content"), 0o600); err != nil {
		t.Fatalf("corrupt the stored artifact %s: %v", casFile, err)
	}

	code, corruptEnvelope, stderr := env.runJSON("run", "verify", id, "--state-dir", env.stateDir)
	if code != 6 {
		t.Fatalf("corrupted verify exit = %d, want 6 (stderr: %s)", code, stderr)
	}
	if got := corruptEnvelope["ok"]; got != false {
		t.Errorf("ok = %v, want false", got)
	}
	failure, ok := corruptEnvelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %v, want an object", corruptEnvelope["error"])
	}
	if hint, _ := failure["hint"].(string); !strings.Contains(hint, sha) {
		t.Errorf("error.hint = %q, want it to name the offending digest", hint)
	}
	corruptVerification, ok := corruptEnvelope["verification"].(map[string]any)
	if !ok {
		t.Fatalf("failed envelope has no verification object: %v", corruptEnvelope)
	}
	problems, ok := corruptVerification["problems"].([]any)
	if !ok || len(problems) == 0 {
		t.Fatalf("verification.problems = %v, want at least one", corruptVerification["problems"])
	}
	first, _ := problems[0].(map[string]any)
	if got, _ := first["sha256"].(string); got != sha {
		t.Errorf("problem sha256 = %v, want %s", first["sha256"], got)
	}
}
