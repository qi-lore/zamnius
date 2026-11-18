package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/qi-lore/zamnius/internal/domain"
)

func TestCanonicalJSONIsOrderIndependent(t *testing.T) {
	t.Parallel()

	first := map[string]any{
		"b": 2,
		"a": 1,
		"nested": map[string]any{
			"z": "last",
			"y": "second",
		},
	}
	second := map[string]any{
		"nested": map[string]any{
			"y": "second",
			"z": "last",
		},
		"a": 1,
		"b": 2,
	}

	left, err := domain.CanonicalJSON(first)
	if err != nil {
		t.Fatalf("CanonicalJSON(first) error = %v", err)
	}
	right, err := domain.CanonicalJSON(second)
	if err != nil {
		t.Fatalf("CanonicalJSON(second) error = %v", err)
	}
	if string(left) != string(right) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", left, right)
	}
	if domain.HashBytes(left) != domain.HashBytes(right) {
		t.Fatal("fingerprints differ for equal maps")
	}

	want := `{"a":1,"b":2,"nested":{"y":"second","z":"last"}}`
	if string(left) != want {
		t.Errorf("canonical encoding = %s, want %s", left, want)
	}
}

func TestCanonicalJSONDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()

	encoded, err := domain.CanonicalJSON(map[string]any{"text": "a<b&c>d"})
	if err != nil {
		t.Fatalf("CanonicalJSON error = %v", err)
	}
	want := `{"text":"a<b&c>d"}`
	if string(encoded) != want {
		t.Errorf("encoding = %s, want %s", encoded, want)
	}
}

func TestCanonicalJSONHasNoTrailingNewline(t *testing.T) {
	t.Parallel()

	encoded, err := domain.CanonicalJSON(map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("CanonicalJSON error = %v", err)
	}
	if len(encoded) == 0 || encoded[len(encoded)-1] == '\n' {
		t.Fatalf("canonical encoding ends with a newline: %q", encoded)
	}
}

func TestCanonicalJSONRoundTripsStructsInFieldOrder(t *testing.T) {
	t.Parallel()

	// Struct field order is the canonical order for struct payloads; the test
	// pins it so an accidental field reorder is caught as a format change.
	type sample struct {
		Zeta  string `json:"zeta"`
		Alpha string `json:"alpha"`
	}
	encoded, err := domain.CanonicalJSON(sample{Zeta: "z", Alpha: "a"})
	if err != nil {
		t.Fatalf("CanonicalJSON error = %v", err)
	}
	if want := `{"zeta":"z","alpha":"a"}`; string(encoded) != want {
		t.Errorf("encoding = %s, want %s", encoded, want)
	}
}

func TestHashAndChainDeterministic(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"a":1}`)
	if domain.HashBytes(payload) != domain.HashBytes([]byte(`{"a":1}`)) {
		t.Fatal("HashBytes is not deterministic")
	}
	if len(domain.HashBytes(payload)) != 64 {
		t.Fatalf("HashBytes length = %d, want 64", len(domain.HashBytes(payload)))
	}

	first := domain.ChainHash("", payload)
	second := domain.ChainHash(first, payload)
	if first == second {
		t.Fatal("chained hashes must differ when the predecessor differs")
	}
	if len(first) != 64 || len(second) != 64 {
		t.Fatal("chain hashes must be 64 hex characters")
	}
	// The rule is sha256(prev || payload) with no separator, so this must be
	// reproducible by anyone reading the spec.
	if want := domain.HashBytes(append([]byte(first), payload...)); want != second {
		t.Errorf("ChainHash does not follow sha256(prev||payload)")
	}
}

func TestVerifyDigest(t *testing.T) {
	t.Parallel()

	content := []byte("# plan\n")
	digest := domain.HashBytes(content)
	if !domain.VerifyDigest(digest, content) {
		t.Error("VerifyDigest rejected matching content")
	}
	if domain.VerifyDigest(digest, []byte("# tampered\n")) {
		t.Error("VerifyDigest accepted mismatched content")
	}
	if domain.VerifyDigest("short", content) {
		t.Error("VerifyDigest accepted a malformed digest")
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	t.Parallel()

	budget := domain.Budget{
		Attempts:  intPtr(3),
		WallClock: durationPtr("45s"),
		Tokens:    intPtr(500),
	}
	encoded, err := domain.CanonicalJSON(budget)
	if err != nil {
		t.Fatalf("CanonicalJSON error = %v", err)
	}
	want := `{"attempts":3,"wall_clock":"45s","tokens":500}`
	if string(encoded) != want {
		t.Fatalf("encoding = %s, want %s", encoded, want)
	}

	var decoded domain.Budget
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if decoded.WallClock == nil || decoded.WallClock.String() != "45s" {
		t.Fatalf("wall clock did not round-trip: %+v", decoded)
	}

	// The restricted syntax must also be enforced through JSON.
	if err := json.Unmarshal([]byte(`{"wall_clock":"1m30s"}`), &decoded); err == nil {
		t.Fatal("accepted a multi-unit duration through JSON")
	}
}

func intPtr(v int) *int { return &v }

func durationPtr(text string) *domain.Duration {
	d, err := domain.ParseDuration(text)
	if err != nil {
		panic(err)
	}
	return &d
}
