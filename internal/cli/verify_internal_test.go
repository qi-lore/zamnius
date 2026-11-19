package cli

// This file covers the verify rendering at the unit level: a report with problems
// must classify as a persistence integrity failure (exit `6`) and carry every
// problem in its hint, and a clean report must print a citable verdict line — the
// same detail the JSON envelope carries (contracts/cli.md §1, §3). The end-to-end
// path through a real run and a real corrupted file is the integration test.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/audit"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/pkg/zamnius"
)

func TestVerificationFailureClassifiesAsIntegrityAndNamesTheProblems(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", 64)
	report := zamnius.VerifyReport{
		RunID:     uuid.MustParse("6f1c1f0e-0000-0000-0000-000000000000"),
		Events:    12,
		Artifacts: 3,
		Problems: []audit.Problem{
			{Kind: audit.ProblemChain, Seq: 5, Detail: "hash mismatch at seq 5"},
			{Kind: audit.ProblemMissingFile, SHA256: digest, Detail: "artifact file " + digest + " is missing"},
		},
	}

	err := verificationFailure(report)
	if !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("verificationFailure() error = %v, want it to wrap domain.ErrIntegrity", err)
	}
	if code := exitCodeFor(err, ""); code != ExitIntegrity {
		t.Errorf("exit code = %d, want %d", code, ExitIntegrity)
	}
	if !strings.Contains(err.Error(), "2 problems") {
		t.Errorf("message %q does not name the problem count", err.Error())
	}
	hint := domain.HintOf(err)
	for _, want := range []string{"hash mismatch at seq 5", "is missing"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not carry %q", hint, want)
		}
	}
}

func TestHumanVerifyPrintsACitableVerdict(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	r := &runner{ctx: context.Background(), stdout: &out, stderr: io.Discard}
	report := zamnius.VerifyReport{
		RunID:     uuid.MustParse("6f1c1f0e-0000-0000-0000-000000000000"),
		Events:    12,
		Artifacts: 3,
	}

	r.humanVerify(report)
	got := out.String()

	for _, want := range []string{"verified", "12 events", "3 artifacts", "all fingerprints match"} {
		if !strings.Contains(got, want) {
			t.Errorf("human verify output %q does not mention %q", got, want)
		}
	}
}
