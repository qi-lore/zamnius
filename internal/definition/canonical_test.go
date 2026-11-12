package definition_test

import (
	"errors"
	"testing"

	"github.com/hIMEI29A/zamnius/internal/definition"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// TestFingerprintIsIdenticalAcrossFormats is the contract of FR-061: the encoding
// an operator chooses is not part of the workflow's identity, so YAML, JSON, and
// TOML versions of one workflow must share a single fingerprint.
func TestFingerprintIsIdenticalAcrossFormats(t *testing.T) {
	t.Parallel()

	yamlDocument := []byte(`
version: "1"
name: parity
description: the same workflow three ways
phases:
  - name: plan
    mode: single
    agent:
      name: planner
      instructions: make a plan
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 512
    outputs: [plan]
`)

	jsonDocument := []byte(`{
  "version": "1",
  "name": "parity",
  "description": "the same workflow three ways",
  "phases": [
    {
      "name": "plan",
      "mode": "single",
      "agent": {
        "name": "planner",
        "instructions": "make a plan",
        "model": {
          "provider": "openai-compatible",
          "name": "gpt-4.1-mini",
          "max_output_tokens": 512
        }
      },
      "outputs": ["plan"]
    }
  ]
}`)

	tomlDocument := []byte(`
version = "1"
name = "parity"
description = "the same workflow three ways"

[[phases]]
name = "plan"
mode = "single"
outputs = ["plan"]

[phases.agent]
name = "planner"
instructions = "make a plan"

[phases.agent.model]
provider = "openai-compatible"
name = "gpt-4.1-mini"
max_output_tokens = 512
`)

	documents := map[string][]byte{
		"yaml": yamlDocument,
		"json": jsonDocument,
		"toml": tomlDocument,
	}

	fingerprints := make(map[string]string, len(documents))
	for name, document := range documents {
		format := definition.Format(name)
		def, err := definition.Decode(document, format)
		if err != nil {
			t.Fatalf("%s: Decode() error = %v", name, err)
		}
		fingerprint, err := definition.Fingerprint(def)
		if err != nil {
			t.Fatalf("%s: Fingerprint() error = %v", name, err)
		}
		fingerprints[name] = fingerprint
	}

	for name, fingerprint := range fingerprints {
		if fingerprint != fingerprints["yaml"] {
			t.Errorf("%s fingerprint = %s, want %s", name, fingerprint, fingerprints["yaml"])
		}
		if len(fingerprint) != 64 {
			t.Errorf("%s fingerprint = %q, want a sha256 hex digest", name, fingerprint)
		}
	}
}

// TestFingerprintIgnoresKeyOrderAndWhitespace proves the identity is over the
// canonical form, not over the bytes an editor happened to write.
func TestFingerprintIgnoresKeyOrderAndWhitespace(t *testing.T) {
	t.Parallel()

	first := []byte(`
version: "1"
name: whitespace
phases:
  - name: plan
    mode: single
    agent:
      name: planner
      instructions: make a plan
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
`)

	second := []byte(`{"phases":[{"agent":{"model":{"name":"gpt-4.1-mini","provider":"openai-compatible"},"instructions":"make a plan","name":"planner"},"mode":"single","name":"plan"}],"name":"whitespace","version":"1"}`)

	left, err := definition.Decode(first, definition.FormatYAML)
	if err != nil {
		t.Fatalf("Decode(first) error = %v", err)
	}
	right, err := definition.Decode(second, definition.FormatJSON)
	if err != nil {
		t.Fatalf("Decode(second) error = %v", err)
	}

	leftPrint, err := definition.Fingerprint(left)
	if err != nil {
		t.Fatalf("Fingerprint(left) error = %v", err)
	}
	rightPrint, err := definition.Fingerprint(right)
	if err != nil {
		t.Fatalf("Fingerprint(right) error = %v", err)
	}
	if leftPrint != rightPrint {
		t.Fatalf("key order or whitespace changed the fingerprint: %s vs %s", leftPrint, rightPrint)
	}
}

// TestFingerprintDetectsChange is the point of fingerprinting at all: a resumed
// run must be able to tell that its definition drifted (data-model.md §10).
func TestFingerprintDetectsChange(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	base, err := definition.NewBuilder("drift").WithPhase("plan", agent).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	basePrint, err := definition.Fingerprint(base)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	changed := agent
	changed.Instructions = "make a different plan"
	drifted, err := definition.NewBuilder("drift").WithPhase("plan", changed).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	driftedPrint, err := definition.Fingerprint(drifted)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	if basePrint == driftedPrint {
		t.Fatal("a changed instruction did not change the fingerprint")
	}
}

// TestSnapshotRoundTrips proves resume needs nothing but the stored snapshot: it
// re-parses to the same definition and the same fingerprint.
func TestSnapshotRoundTrips(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	original, err := definition.NewBuilder("round-trip").
		Describe("snapshotted").
		WithBudget(domain.Budget{Attempts: intPtr(4)}).
		WithPhase("plan", agent, definition.WithOutputs("plan"), definition.WithRetry(definition.Retry{MaxAttempts: 2})).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	snapshot, err := definition.Snapshot(original)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	restored, err := definition.FromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("FromSnapshot() error = %v", err)
	}

	want, err := definition.Fingerprint(original)
	if err != nil {
		t.Fatalf("Fingerprint(original) error = %v", err)
	}
	got, err := definition.Fingerprint(restored)
	if err != nil {
		t.Fatalf("Fingerprint(restored) error = %v", err)
	}
	if want != got {
		t.Fatalf("snapshot round-trip changed the fingerprint: %s then %s", want, got)
	}
	if restored.Phases[0].Retry.MaxAttempts != 2 {
		t.Errorf("resolved retry lost on round-trip: %+v", restored.Phases[0].Retry)
	}
}

func TestFromSnapshotRejectsCorruptSnapshot(t *testing.T) {
	t.Parallel()

	_, err := definition.FromSnapshot([]byte(`{"version":"1","name":`))
	if err == nil {
		t.Fatal("FromSnapshot accepted a truncated snapshot")
	}
	if got := domain.ClassOf(err); got != domain.ErrClassInternal {
		t.Errorf("class = %q, want internal", got)
	}
	if !errors.Is(err, domain.ErrIntegrity) {
		t.Errorf("error %v must wrap ErrIntegrity", err)
	}
}

// TestSnapshotIsCanonical asserts the stored bytes are stable and compact, which
// is what lets two processes compare fingerprints without re-encoding.
func TestSnapshotIsCanonical(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 0)
	def, err := definition.NewBuilder("canonical").WithPhase("plan", agent).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	first, err := definition.Snapshot(def)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	second, err := definition.Snapshot(def)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Snapshot is not deterministic")
	}
	if len(first) == 0 || first[0] != '{' {
		t.Fatalf("snapshot is not a JSON object: %s", first)
	}
}

// TestInputFingerprintIsStableAndSensitive covers the identifier attempt
// atomicity relies on: it must depend only on inputs, never on time.
func TestInputFingerprintIsStableAndSensitive(t *testing.T) {
	t.Parallel()

	agent := definition.Agent("planner", "make a plan", "gpt-4.1-mini", 512)
	def, err := definition.NewBuilder("fingerprint").
		WithPhase("plan", agent, definition.WithOutputs("plan")).
		WithPhase("build", agent).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	upstream := []string{domain.HashBytes([]byte("plan-artifact"))}

	first, err := definition.InputFingerprint(def, 1, upstream)
	if err != nil {
		t.Fatalf("InputFingerprint() error = %v", err)
	}
	again, err := definition.InputFingerprint(def, 1, upstream)
	if err != nil {
		t.Fatalf("InputFingerprint() error = %v", err)
	}
	if first != again {
		t.Fatalf("input fingerprint is not deterministic: %s then %s", first, again)
	}

	// A different upstream artifact means different inputs, so the fingerprint must
	// differ; this is what makes redundant re-execution detectable.
	other, err := definition.InputFingerprint(def, 1, []string{domain.HashBytes([]byte("different"))})
	if err != nil {
		t.Fatalf("InputFingerprint() error = %v", err)
	}
	if first == other {
		t.Fatal("a changed upstream artifact did not change the input fingerprint")
	}

	if _, err := definition.InputFingerprint(def, 9, upstream); err == nil {
		t.Fatal("InputFingerprint accepted an out-of-range phase index")
	}
}

func TestPromptVersionTracksInstructionText(t *testing.T) {
	t.Parallel()

	first := definition.PromptVersion("make a plan")
	second := definition.PromptVersion("make a plan")
	changed := definition.PromptVersion("make a different plan")

	if first != second {
		t.Errorf("PromptVersion is not deterministic: %s then %s", first, second)
	}
	if first == changed {
		t.Error("a changed instruction produced the same prompt version")
	}
	if len(first) < len("inline:sha256:")+64 {
		t.Errorf("PromptVersion = %q, want an inline sha256 reference", first)
	}
}
