package definition_test

import (
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
)

// TestDecodeRefusesBaseURLInADefinition is the F1 fix: v0.1.0 accepted the key and
// silently ignored it, so the endpoint came from the environment whatever the file
// said. It is now refused, and the hint names the variable that actually works.
func TestDecodeRefusesBaseURLInADefinition(t *testing.T) {
	t.Parallel()

	doc := strings.Replace(onePhaseJSON,
		`"name": "gpt-4.1-mini"`,
		`"name": "gpt-4.1-mini", "base_url": "http://127.0.0.1:8080/v1"`, 1)

	_, err := definition.Decode([]byte(doc), definition.FormatJSON)
	if err == nil {
		t.Fatalf("Decode() accepted a definition that sets model.base_url")
	}
	hint := domain.HintOf(err)
	if !strings.HasPrefix(hint, "phases[0].agent.model.base_url:") {
		t.Errorf("hint = %q, want it to name phases[0].agent.model.base_url", hint)
	}
	if !strings.Contains(hint, domain.ModelBaseURLEnvVar) {
		t.Errorf("hint = %q, want it to name %s", hint, domain.ModelBaseURLEnvVar)
	}
}

// TestFromSnapshotStillDecodesAV010Snapshot keeps the refusal above from breaking
// resumability: a run started by v0.1.0 from such a definition recorded the key in
// its snapshot, and that run must still resume.
func TestFromSnapshotStillDecodesAV010Snapshot(t *testing.T) {
	t.Parallel()

	doc := strings.Replace(onePhaseJSON,
		`"name": "gpt-4.1-mini"`,
		`"name": "gpt-4.1-mini", "base_url": "http://127.0.0.1:8080/v1"`, 1)

	def, err := definition.FromSnapshot([]byte(doc))
	if err != nil {
		t.Fatalf("FromSnapshot() error = %v, want a v0.1.0 snapshot to decode", err)
	}
	if len(def.Phases) != 1 || def.Phases[0].Agent == nil {
		t.Fatalf("snapshot decoded to %+v, want the stored phase", def)
	}
}

// TestSnapshotKeepsTheLegacyFieldOrder pins the byte order of a snapshot that
// carries model.base_url. A fingerprint is the hash of these exact bytes, so
// reordering the legacy field in ModelRef would silently invalidate the stored
// fingerprints of runs started by v0.1.0 from such a definition.
func TestSnapshotKeepsTheLegacyFieldOrder(t *testing.T) {
	t.Parallel()

	def := definition.Definition{
		Version: "1",
		Name:    "legacy",
		Phases: []definition.Phase{{
			Name: "plan",
			Mode: definition.ModeSingle,
			Agent: &definition.AgentRef{
				Name:         "planner",
				Instructions: "make a plan",
				Model: definition.ModelRef{
					Provider:        "openai-compatible",
					Name:            "gpt-4.1-mini",
					BaseURL:         "http://127.0.0.1:8080/v1",
					MaxOutputTokens: 512,
				},
			},
		}},
	}

	snapshot, err := definition.Snapshot(def)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	want := `"model":{"provider":"openai-compatible","name":"gpt-4.1-mini","base_url":"http://127.0.0.1:8080/v1","max_output_tokens":512}`
	if !strings.Contains(string(snapshot), want) {
		t.Fatalf("snapshot = %s\nwant it to contain %s", snapshot, want)
	}
}
