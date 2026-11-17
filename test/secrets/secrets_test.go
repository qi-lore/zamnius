package secrets_test

// test/secrets/secrets_test.go — T063: the secret-leak suite (SC-014, FR-054).
//
// The suite runs the offline corpus with ZAMNIUS_MODEL_API_KEY set to a canary, in
// a real child process whose streams it captures, then scans every surface the
// corpus produced for that canary and for an Authorization value:
//
//   - the JSON envelope each command wrote to stdout,
//   - the log lines each command wrote to stderr,
//   - every file under the run's root: artifact files, the exported copies, the
//     SQLite store and its write-ahead log,
//   - every event payload, decision record, attempt row, and artifact row read
//     back through the store.
//
// Zero findings is the assertion; a finding names the surface it came from, so a
// leak is actionable without re-running the suite.
//
// The process boundary is deliberate rather than decorative: the composition root
// binds its logger to os.Stderr (internal/app/app.go), so a real process whose
// streams are captured is what makes the log lines the lines the shipped binary
// writes. quickstart.md scenario 8 shows the same run from a shell.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/app"
	"github.com/prompt-fu/zamnius/internal/cli"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/observability"
	"github.com/prompt-fu/zamnius/internal/store/sqlite"
)

// canaryKey is the credential every command in the corpus runs with. It is a
// stand-in, never a real key, and it is the value SC-014 requires to appear
// nowhere outside the process environment.
const canaryKey = "sk-test-canary"

// The child-process handshake. The suite re-executes its own binary to run a batch
// of commands in one real process; the script lists them, and the capture directory
// receives one file set — stdout, stderr, and exit code — per command. The two
// separators are control characters, so no argument can be split or re-joined by
// accident.
const (
	childScript   = "ZAMNIUS_SECRETS_SCRIPT"
	childCaptures = "ZAMNIUS_SECRETS_CAPTURES"
	commandSep    = "\x1e"
	argSep        = "\x1f"
)

// TestSecretsChild is not an ordinary test: it is the entry point the suite
// re-executes so its commands run in a real process. For each command it points
// os.Stdout and os.Stderr at that command's own capture files and calls the shipped
// entry point, so what lands in the capture is the JSON envelope and the log lines
// the binary really writes. Without the trigger it skips, so an ordinary `go test`
// run never executes it.
//
// The child reports only its own failures: a command's non-zero exit is a corpus
// outcome the parent asserts, not something to abort on. It returns rather than
// exits so the testing framework keeps the process's real exit status, which is what
// carries a race the detector found in the child back to the parent.
func TestSecretsChild(t *testing.T) {
	captures := os.Getenv(childCaptures)
	if captures == "" {
		t.Skip("only runs as the child of the secrets suite")
	}

	for index, command := range strings.Split(os.Getenv(childScript), commandSep) {
		if err := captureCommand(captures, index, strings.Split(command, argSep)); err != nil {
			t.Fatalf("capture command %d: %v", index, err)
		}
	}
}

// captureCommand runs one command with its streams pointed at its own files, and
// records the exit code beside them. Replacing os.Stderr is what routes the log
// lines into the capture: the composition root reads that variable when it builds
// the logger.
func captureCommand(dir string, index int, args []string) error {
	stdout, err := os.Create(filepath.Join(dir, fmt.Sprintf("%02d.stdout", index)))
	if err != nil {
		return fmt.Errorf("create the capture: %w", err)
	}
	defer func() { _ = stdout.Close() }()

	stderr, err := os.Create(filepath.Join(dir, fmt.Sprintf("%02d.stderr", index)))
	if err != nil {
		return fmt.Errorf("create the capture: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	previousOut, previousErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code := cli.Run(context.Background(), args, os.Stdout, os.Stderr)
	os.Stdout, os.Stderr = previousOut, previousErr

	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.exit", index)), []byte(strconv.Itoa(code)), 0o600); err != nil {
		return fmt.Errorf("record the exit code: %w", err)
	}
	return nil
}

// forbiddenValue is one value SC-014 forbids on every surface. The credential is
// matched exactly because a key is case-sensitive; the Authorization header name is
// matched case-insensitively because writers spell it differently and either
// spelling is the disclosure the contract forbids.
type forbiddenValue struct {
	name     string
	needle   string
	foldCase bool
}

var forbiddenValues = []forbiddenValue{
	{name: "the provider credential", needle: canaryKey},
	{name: "an Authorization header name", needle: "authorization", foldCase: true},
}

// surface is one place a forbidden value could appear: a captured stream, a file
// on disk, or a record read back from the store.
type surface struct {
	name    string
	content []byte
}

// finding is one forbidden value seen on one surface.
type finding struct {
	surface string
	value   string
	excerpt string
}

// scanSurface searches one surface for every forbidden value. A value is reported
// once per surface: more matches of one value say nothing new.
func scanSurface(s surface) []finding {
	var found []finding
	for _, forbidden := range forbiddenValues {
		haystack, needle := string(s.content), forbidden.needle
		if forbidden.foldCase {
			// The needles are ASCII, so lowercasing keeps the byte offsets the
			// excerpt is built from.
			haystack, needle = strings.ToLower(haystack), strings.ToLower(needle)
		}
		index := strings.Index(haystack, needle)
		if index < 0 {
			continue
		}
		found = append(found, finding{
			surface: s.name,
			value:   forbidden.name,
			excerpt: excerptAt(string(s.content), index),
		})
	}
	return found
}

// scanJSON encodes one record and scans it, so the scan never has to enumerate the
// fields of the row it reads.
func scanJSON(name string, value any) ([]finding, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", name, err)
	}
	return scanSurface(surface{name: name, content: encoded}), nil
}

// excerptAt returns a bounded, single-line window around a match, so a failure
// shows enough of the offending record to act on without dumping it whole.
func excerptAt(content string, index int) string {
	const window = 48

	start := max(0, index-window)
	end := min(len(content), index+window)
	excerpt := strings.ReplaceAll(content[start:end], "\n", `\n`)
	if start > 0 {
		excerpt = "..." + excerpt
	}
	if end < len(content) {
		excerpt += "..."
	}
	return excerpt
}

// assertNoFindings fails with every finding rather than only the first, so one run
// reports every leak the corpus exposed.
func assertNoFindings(t *testing.T, findings []finding) {
	t.Helper()

	if len(findings) == 0 {
		return
	}
	lines := make([]string, 0, len(findings))
	for _, found := range findings {
		lines = append(lines, fmt.Sprintf("%s on %s: %s", found.value, found.surface, found.excerpt))
	}
	t.Errorf("SC-014: %d secret finding(s):\n%s", len(findings), strings.Join(lines, "\n"))
}

// answeringProvider is a deterministic stand-in for an OpenAI-compatible service.
// It reaches nothing but the loopback socket, and it answers each call with
// different content so a fan-out's branches do not deduplicate to one artifact
// (identical bytes are one content address, and one artifact would hide surface).
type answeringProvider struct{ calls atomic.Int32 }

func (p *answeringProvider) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w,
		`{"choices":[{"message":{"role":"assistant","content":"answer %d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`,
		p.calls.Add(1))
}

// refusingProvider rejects every call and echoes the credential the caller
// presented, the way a provider that quotes its rejected request would. It is the
// adversarial case: the credential is in the provider's hands, so redaction has to
// survive provider text travelling into an error hint, an event payload, an attempt
// row, and a log line.
type refusingProvider struct{}

func (refusingProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":{"message":"invalid api key %s"}}`, r.Header.Get("Authorization"))
}

// capture is one command's outcome, read back from the child's capture files.
type capture struct {
	label  string
	exit   int
	stdout string
	stderr string
}

// secretsEnv is one isolated corpus environment: the root every file it writes
// lives under, the state directory the composition root is pointed at, an export
// directory, and the capture directory the child writes into.
type secretsEnv struct {
	t         *testing.T
	baseURL   string
	root      string
	stateDir  string
	exportDir string
	captures  string
	queued    []string
	labels    []string
	surfaces  []surface
	// logStreams counts the commands whose stderr carried at least one log line.
	// The suite asserts it is non-zero: a log surface that captured nothing would
	// make the sweep over it pass for the wrong reason.
	logStreams int
}

// newSecretsEnv wires the environment a command inherits: an offline provider, a
// state directory, and an export directory. The state and export directories live
// under one root so the file sweep covers everything the corpus wrote there; the
// captures live outside it, because the captured streams are already scanned in
// memory and sweeping them twice would report one leak twice.
func newSecretsEnv(t *testing.T, provider http.Handler) *secretsEnv {
	t.Helper()

	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)

	root := t.TempDir()
	return &secretsEnv{
		t:         t,
		baseURL:   server.URL,
		root:      root,
		stateDir:  filepath.Join(root, "state"),
		exportDir: filepath.Join(root, "export"),
		captures:  filepath.Join(t.TempDir(), "captures"),
	}
}

// queue adds one command to the batch the next execute runs.
func (e *secretsEnv) queue(label string, args ...string) {
	e.queued = append(e.queued, strings.Join(args, argSep))
	e.labels = append(e.labels, label)
}

// execute runs every queued command in one child process, records both streams of
// each as surfaces, and returns the captures in queue order. A failure to run the
// child at all is a harness failure: the child itself never fails on a command.
func (e *secretsEnv) execute() []capture {
	e.t.Helper()

	if len(e.queued) == 0 {
		return nil
	}
	if err := os.MkdirAll(e.captures, 0o750); err != nil {
		e.t.Fatalf("create the capture directory: %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestSecretsChild$")
	child.Env = childEnv(e.baseURL, e.captures, e.queued)

	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			e.t.Fatalf("run the secrets child: %v", err)
		}
		e.t.Fatalf("the secrets child exited %d\nstdout: %s\nstderr: %s",
			exit.ExitCode(), stdout.String(), stderr.String())
	}

	captures := make([]capture, 0, len(e.queued))
	for index, label := range e.labels {
		entry := readCapture(e.t, e.captures, index, label)
		captures = append(captures, entry)
		e.surfaces = append(e.surfaces,
			surface{name: "stdout of `" + label + "`", content: []byte(entry.stdout)},
			surface{name: "stderr of `" + label + "`", content: []byte(entry.stderr)},
		)
		if entry.stderr != "" {
			e.logStreams++
		}
	}
	e.queued, e.labels = nil, nil
	return captures
}

// readCapture reads one command's three capture files.
func readCapture(t *testing.T, dir string, index int, label string) capture {
	t.Helper()

	read := func(suffix string) string {
		content, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%02d.%s", index, suffix)))
		if err != nil {
			t.Fatalf("read the %s capture of %s: %v", suffix, label, err)
		}
		return string(content)
	}

	code, err := strconv.Atoi(strings.TrimSpace(read("exit")))
	if err != nil {
		t.Fatalf("the exit capture of %s is not a number: %v", label, err)
	}
	return capture{label: label, exit: code, stdout: read("stdout"), stderr: read("stderr")}
}

// childEnv is the child's whole environment. Every inherited ZAMNIUS_* variable is
// dropped so the corpus cannot pick up a real credential, a stale state directory,
// or a log setting from whoever runs the suite; the canary, the provider root, and
// the batch to run are then the only values it can act on.
func childEnv(baseURL, captures string, script []string) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ZAMNIUS_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		observability.SecretEnvVar+"="+canaryKey,
		app.BaseURLEnvVar+"="+baseURL,
		childCaptures+"="+captures,
		childScript+"="+strings.Join(script, commandSep),
		// Debug is the widest log surface the framework has, so the corpus proves
		// the leak test over the most verbose configuration rather than the quietest.
		"ZAMNIUS_LOG_LEVEL=debug",
		"ZAMNIUS_LOG_FORMAT=json",
	)
}

// corpusCase is one offline run: the definition file and the exit code the run must
// reach. The exit code is part of the corpus rather than decoration, because a run
// that stopped for another reason would not produce the surface the case exists for.
type corpusCase struct {
	name     string
	file     string
	wantExit int
	// createsRun marks a case whose command created a run, so the read path is
	// exercised over it too: a refused definition creates none, and its surfaces are
	// the envelope and the log lines alone.
	createsRun bool
}

// fixture resolves a workflow fixture that ships with the repository, by absolute
// path so the child process cannot be affected by the working directory.
func fixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "testdata", "workflows", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s is missing: %v", path, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve fixture %s: %v", path, err)
	}
	return absolute
}

// budgetStopWorkflow writes the one definition the shipped fixtures do not cover: a
// run whose one-attempt budget stops its second phase. It is written to the test's
// own root rather than added to test/testdata, because it exists only to reach the
// budget-stop record — the decision and the event a leak could hide in.
func budgetStopWorkflow(t *testing.T, root string) string {
	t.Helper()

	const definition = `version: "1"
name: budget-stop
description: Two phases whose one-attempt run budget stops the second one.
budget:
  attempts: 1
phases:
  - name: draft
    mode: single
    agent:
      name: writer
      instructions: Write one short draft.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 128
    outputs:
      - plan
  - name: review
    mode: single
    depends_on:
      - draft
    agent:
      name: reviewer
      instructions: Review the draft.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 128
    outputs:
      - report
`

	path := filepath.Join(root, "budget-stop.yaml")
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatalf("write the budget-stop definition: %v", err)
	}
	return path
}

// runIDOf reads the run identifier out of a start envelope.
func runIDOf(t *testing.T, stdout string) string {
	t.Helper()

	var envelope struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("start envelope is not JSON: %v\noutput: %s", err, stdout)
	}
	if envelope.Run.ID == "" {
		t.Fatalf("start envelope carries no run id: %s", stdout)
	}
	return envelope.Run.ID
}

// TestNoSecretLeaksFromTheOfflineCorpus is T063's central assertion: the whole
// offline corpus runs with the canary in the environment, and no surface it
// produced carries the canary or an Authorization value (SC-014, FR-054).
func TestNoSecretLeaksFromTheOfflineCorpus(t *testing.T) {
	env := newSecretsEnv(t, &answeringProvider{})

	corpus := []corpusCase{
		{name: "hello.yaml", file: fixture(t, "hello.yaml"), wantExit: cli.ExitOK, createsRun: true},
		{name: "hello.json", file: fixture(t, "hello.json"), wantExit: cli.ExitOK, createsRun: true},
		{name: "hello.toml", file: fixture(t, "hello.toml"), wantExit: cli.ExitOK, createsRun: true},
		{name: "fanout.yaml", file: fixture(t, "fanout.yaml"), wantExit: cli.ExitOK, createsRun: true},
		{name: "slow.yaml", file: fixture(t, "slow.yaml"), wantExit: cli.ExitOK, createsRun: true},
		// The 40-token budget survives one 18-token call, so this shipped fixture
		// succeeds and only its run-level budget record is added to the sweep. The
		// budget-stop case below is what reaches the budget-stop decision.
		{name: "budget-tokens.yaml", file: fixture(t, "budget-tokens.yaml"), wantExit: cli.ExitOK, createsRun: true},
		{name: "budget-stop", file: budgetStopWorkflow(t, env.root), wantExit: cli.ExitRunFailed, createsRun: true},
		// A refused definition is a surface too: it never reaches a provider, yet the
		// envelope and the log lines it produced are still scanned.
		{name: "invalid.yaml", file: fixture(t, "invalid.yaml"), wantExit: cli.ExitInvalid},
	}

	started := make([]string, 0, len(corpus))
	for _, testCase := range corpus {
		env.queue("run start "+testCase.name, "run", "start", "--file", testCase.file, "--state-dir", env.stateDir, "--json")
	}
	starts := env.execute()

	for index, testCase := range corpus {
		if got := starts[index].exit; got != testCase.wantExit {
			t.Fatalf("run start %s exit = %d, want %d\nstdout: %s",
				testCase.name, got, testCase.wantExit, starts[index].stdout)
		}
		if testCase.createsRun {
			started = append(started, runIDOf(t, starts[index].stdout))
		}
	}

	// The inspection commands are the read path an operator uses, and their envelopes
	// carry the provenance an auditor reads: status reports the position and the last
	// events, the export copies every artifact file.
	for _, runID := range started {
		env.queue("run status "+runID, "run", "status", runID, "--state-dir", env.stateDir, "--json")
		env.queue("run artifacts "+runID, "run", "artifacts", runID, "--state-dir", env.stateDir,
			"--out", filepath.Join(env.exportDir, runID), "--json")
	}
	// A resume of a terminal run is the recovery path's no-op branch: it executes
	// nothing, so its envelope is the only surface it adds.
	env.queue("run resume "+started[0], "run", "resume", started[0], "--state-dir", env.stateDir, "--json")
	inspections := env.execute()

	for _, entry := range inspections {
		if entry.exit != cli.ExitOK {
			t.Fatalf("%s exit = %d, want %d\nstdout: %s", entry.label, entry.exit, cli.ExitOK, entry.stdout)
		}
	}

	findings, report := env.auditCorpus(t)

	// The corpus has to be non-vacuous: a leak test that scanned nothing would pass
	// for the wrong reason, so the surfaces it read are asserted first.
	if want := 2 * (len(starts) + len(inspections)); report.streams != want {
		t.Fatalf("captured %d streams, want both streams of all %d commands", report.streams, want/2)
	}
	if report.logStreams == 0 {
		t.Fatal("no command wrote a log line; the log sweep would prove nothing")
	}
	if report.files == 0 || report.runs == 0 || report.events == 0 || report.decisions == 0 ||
		report.attempts == 0 || report.artifacts == 0 {
		t.Fatalf("the corpus produced nothing to scan: %+v", report)
	}

	assertNoFindings(t, findings)
}

// TestRedactionSurvivesAProviderThatEchoesTheCredential is the adversarial half of
// SC-014: a provider that quotes the credential it rejected must not be able to push
// it into an error hint, an event payload, an attempt row, or a log line. The
// redaction marker is asserted positively, so a suite that silently stopped seeing
// the provider's text would fail rather than pass.
func TestRedactionSurvivesAProviderThatEchoesTheCredential(t *testing.T) {
	env := newSecretsEnv(t, refusingProvider{})

	env.queue("run start hello.yaml", "run", "start", "--file", fixture(t, "hello.yaml"),
		"--state-dir", env.stateDir, "--json")
	start := env.execute()[0]
	if start.exit != cli.ExitRunFailed {
		t.Fatalf("run start exit = %d, want %d\nstdout: %s", start.exit, cli.ExitRunFailed, start.stdout)
	}

	var envelope struct {
		Error struct {
			Class string `json:"class"`
			Hint  string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(start.stdout), &envelope); err != nil {
		t.Fatalf("failure envelope is not JSON: %v\noutput: %s", err, start.stdout)
	}
	if envelope.Error.Class != string(domain.ErrClassAuth) {
		t.Errorf("error.class = %q, want the auth class", envelope.Error.Class)
	}
	if !strings.Contains(envelope.Error.Hint, observability.RedactedValue) {
		t.Errorf("error.hint = %q, want the provider's echoed credential redacted", envelope.Error.Hint)
	}

	findings, report := env.auditCorpus(t)
	if report.events == 0 {
		t.Fatalf("the failing run recorded no event to scan: %+v", report)
	}
	assertNoFindings(t, findings)
}

// TestTheScannerFindsEachForbiddenValue guards the suite itself: the needles have to
// match what they claim to match, otherwise a green run would prove nothing. It is
// the mutation check for a scanner, which cannot be mutated from outside.
func TestTheScannerFindsEachForbiddenValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"the credential itself", "authorization: Bearer " + canaryKey, "the provider credential"},
		{"the header name", "Authorization: Bearer token", "an Authorization header name"},
		{"the header name in another case", "authorization header", "an Authorization header name"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found := scanSurface(surface{name: "test", content: []byte(testCase.content)})
			for _, finding := range found {
				if finding.value == testCase.want {
					return
				}
			}
			t.Errorf("scanSurface(%q) = %v, want a %q finding", testCase.content, found, testCase.want)
		})
	}

	// The clean case: ordinary content must produce no finding, or every corpus run
	// would be reported as a leak.
	if found := scanSurface(surface{name: "test", content: []byte("answer 1")}); len(found) != 0 {
		t.Errorf("scanSurface() = %v, want no finding for ordinary content", found)
	}
}

// corpusReport counts what the scans actually read, so a corpus that produced
// nothing can be told from a corpus that produced something clean.
type corpusReport struct {
	streams    int
	logStreams int
	files      int
	runs       int
	events     int
	decisions  int
	attempts   int
	artifacts  int
}

// auditCorpus scans every surface the corpus produced: the captured streams, every
// file under the environment root, and every record read back through the store.
func (e *secretsEnv) auditCorpus(t *testing.T) ([]finding, corpusReport) {
	t.Helper()

	report := corpusReport{streams: len(e.surfaces), logStreams: e.logStreams}
	findings := make([]finding, 0)
	for _, captured := range e.surfaces {
		findings = append(findings, scanSurface(captured)...)
	}

	// Every file the corpus wrote: the artifact root, the exported copies, the
	// SQLite store, and its write-ahead log. Reading the bytes is what makes this
	// independent of the schema: a column the record scan below does not enumerate
	// is still searched.
	err := filepath.WalkDir(e.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		report.files++
		findings = append(findings, scanSurface(surface{
			name:    "file " + relativeLabel(e.root, path),
			content: content,
		})...)
		return nil
	})
	if err != nil {
		t.Fatalf("sweep %s: %v", e.root, err)
	}

	store, err := sqlite.Open(context.Background(), app.PathsFor(e.stateDir).Database)
	if err != nil {
		t.Fatalf("open the state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	recordFindings, record := scanRecord(t, store)
	findings = append(findings, recordFindings...)
	report.runs, report.events, report.decisions = record.runs, record.events, record.decisions
	report.attempts, report.artifacts = record.attempts, record.artifacts
	return findings, report
}

// relativeLabel names a swept file by its path under the root, so a finding is
// readable and does not depend on the temporary directory's name.
func relativeLabel(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return relative
}

// scanRecord reads every run's events, decision records, attempts, and artifact rows
// through the store and scans each one. The file sweep already covers the database's
// bytes; reading the rows is what lets a finding name the record it came from
// (constitution Principle III: the record is the source of truth, so the suite reads
// what the writer wrote rather than the file that happens to hold it).
func scanRecord(t *testing.T, store *sqlite.Store) ([]finding, corpusReport) {
	t.Helper()

	ctx := context.Background()
	runIDs := allRunIDs(t, store)

	var findings []finding
	report := corpusReport{runs: len(runIDs)}
	for _, runID := range runIDs {
		events, err := store.Events(ctx, runID, 0)
		if err != nil {
			t.Fatalf("Events(%s) error = %v", runID, err)
		}
		for _, event := range events {
			report.events++
			found, encodeErr := scanJSON(fmt.Sprintf("event #%d %s of run %s", event.Seq, event.Type, runID), event)
			if encodeErr != nil {
				t.Fatalf("%v", encodeErr)
			}
			findings = append(findings, found...)
		}

		decisions, err := store.Decisions(ctx, runID)
		if err != nil {
			t.Fatalf("Decisions(%s) error = %v", runID, err)
		}
		for _, decision := range decisions {
			report.decisions++
			found, encodeErr := scanJSON(fmt.Sprintf("decision %s of run %s", decision.Kind, runID), decision)
			if encodeErr != nil {
				t.Fatalf("%v", encodeErr)
			}
			findings = append(findings, found...)
		}

		phases, err := store.Phases(ctx, runID)
		if err != nil {
			t.Fatalf("Phases(%s) error = %v", runID, err)
		}
		for _, phase := range phases {
			attempts, err := store.Attempts(ctx, runID, phase.Name)
			if err != nil {
				t.Fatalf("Attempts(%s, %s) error = %v", runID, phase.Name, err)
			}
			for _, attempt := range attempts {
				report.attempts++
				found, encodeErr := scanJSON(fmt.Sprintf("attempt %s#%d of run %s", attempt.Phase, attempt.AttemptNo, runID), attempt)
				if encodeErr != nil {
					t.Fatalf("%v", encodeErr)
				}
				findings = append(findings, found...)
			}
		}

		refs, err := store.Artifacts(ctx, runID)
		if err != nil {
			t.Fatalf("Artifacts(%s) error = %v", runID, err)
		}
		for _, ref := range refs {
			report.artifacts++
			found, encodeErr := scanJSON(fmt.Sprintf("artifact %s (%s) of run %s", ref.Kind, ref.SHA256, runID), ref)
			if encodeErr != nil {
				t.Fatalf("%v", encodeErr)
			}
			findings = append(findings, found...)
		}
	}
	return findings, report
}

// allRunIDs lists every run in the store, including the ones that failed: a failed
// run's record is as much a surface as a successful one's.
func allRunIDs(t *testing.T, store *sqlite.Store) []uuid.UUID {
	t.Helper()

	rows, err := store.DB().QueryContext(context.Background(), `SELECT id FROM runs ORDER BY created_at`)
	if err != nil {
		t.Fatalf("list the runs: %v", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan a run id: %v", err)
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("run id %q is not a UUID: %v", id, err)
		}
		ids = append(ids, parsed)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the run ids: %v", err)
	}
	return ids
}
