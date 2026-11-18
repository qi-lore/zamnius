package cli

// This file covers T061's status rendering at the unit level: the last five events
// the status view carries must reach the human form, not only the JSON envelope
// (contracts/cli.md §1). The JSON shape is covered by the audit views suite (T058)
// and asserted end to end by T062.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestHumanStatusPrintsTheLastEvents(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	r := &runner{ctx: context.Background(), stdout: &out, stderr: io.Discard}
	view := &runView{
		ID:     "6f1c1f0e-0000-0000-0000-000000000000",
		Status: "succeeded",
		Phases: []phaseView{{Name: "plan", Status: "succeeded", Attempts: 1}},
		LastEvents: []eventView{
			{Seq: 3, Type: "phase.succeeded", Actor: "engine", Phase: "plan"},
			{Seq: 4, Type: "run.succeeded", Actor: "engine"},
		},
	}

	r.humanStatus(view)
	got := out.String()

	if !strings.Contains(got, "last events:") {
		t.Errorf("human status output %q does not introduce the last events", got)
	}
	for _, want := range []string{"phase.succeeded", "run.succeeded", "succeeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("human status output %q does not mention %q", got, want)
		}
	}
}
