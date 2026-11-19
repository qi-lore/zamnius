package cli

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/pkg/zamnius"
)

// runVerify executes `zamnius run verify`: it recomputes the run's event chain and
// re-hashes every committed artifact against its recorded fingerprint, reporting
// whatever disagrees. Like `status` it is read-only — it takes no lease and needs
// no credential — so a record stays checkable long after the key that produced it
// has expired. This is the command-line counterpart of the library's Verify, which
// is what the parity criterion asks for (contracts/cli.md §1, SC-011).
func (r *runner) runVerify(args []string) int {
	return r.inspectCommand("run.verify", args, func(engine *zamnius.Engine, runID uuid.UUID) int {
		report, err := engine.Verify(r.ctx, runID)
		if err != nil {
			return r.finish("run.verify", nil, nil, "", err, nil)
		}
		// The report is attached before rendering so the envelope carries the
		// structured problems in both outcomes, success and failure alike.
		r.verification = &report

		if report.OK() {
			return r.finish("run.verify", nil, nil, "", nil, func() { r.humanVerify(report) })
		}
		return r.finish("run.verify", nil, nil, "", verificationFailure(report), nil)
	})
}

// humanVerify prints the prose form of a clean verification: the run, the two
// counts, and the verdict. It carries the same data the `--json` verification
// section does, so the two renderings cannot drift (contracts/cli.md §1).
func (r *runner) humanVerify(report zamnius.VerifyReport) {
	writeLine(r.stdout, "run %s verified: %s, %s, all fingerprints match",
		report.RunID, plural(report.Events, "event"), plural(report.Artifacts, "artifact"))
}

// verificationFailure turns a report with problems into the classified error the
// contract requires: a persistence integrity failure (exit `6`) whose message names
// how many problems there are and whose hint lists every one, so an operator reading
// stderr sees the same detail the JSON envelope carries (contracts/cli.md §2, §3).
func verificationFailure(report zamnius.VerifyReport) error {
	return &domain.Error{
		Class: domain.ErrClassInternal,
		Msg: fmt.Sprintf("run %s failed verification: %s",
			report.RunID, plural(len(report.Problems), "problem")),
		Hint: problemDetails(report),
		Err:  domain.ErrIntegrity,
	}
}

// problemDetails joins the problems' own details into one actionable hint. The
// details already name the seq or digest that disagrees, so the hint needs to add
// nothing but the separator.
func problemDetails(report zamnius.VerifyReport) string {
	details := make([]string, 0, len(report.Problems))
	for _, problem := range report.Problems {
		details = append(details, problem.Detail)
	}
	return strings.Join(details, "; ")
}
