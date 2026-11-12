// Package audit reconstructs and verifies what a run did from persisted records
// alone. It never stores a second source of truth: every view is computed on
// demand from the store, because a cached projection is exactly the thing an
// auditor cannot trust (Principle III).
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Problem kinds, so a caller can distinguish a tampered chain from a deleted
// file without parsing prose.
const (
	ProblemChain          = "chain"
	ProblemMissingFile    = "missing_file"
	ProblemDigestMismatch = "digest_mismatch"
)

// Problem is one reason verification failed, with enough detail for an operator
// to act: the seq or digest, what was expected, and what was found.
type Problem struct {
	Kind   string `json:"kind"`
	Seq    int64  `json:"seq,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Detail string `json:"detail"`
}

// Report is the result of verifying a run. It is returned even when verification
// fails, so the caller keeps the counts it needs to report.
type Report struct {
	RunID      uuid.UUID `json:"run_id"`
	Events     int       `json:"events"`
	Artifacts  int       `json:"artifacts"`
	VerifiedAt time.Time `json:"verified_at"`
	Problems   []Problem `json:"problems,omitempty"`
}

// OK reports whether the record verified cleanly.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// ArtifactVerifier is the port verification needs; the artifact store satisfies it.
type ArtifactVerifier interface {
	Verify(ctx context.Context, sha string) error
}

// VerifyChain recomputes every event hash and checks the links between events.
// A deleted event shows up as a seq gap or as a prev_hash that points at a hash
// no surviving event carries; both are reported, because silently accepting
// either would make the record unfalsifiable.
func VerifyChain(events []domain.Event) []Problem {
	problems := make([]Problem, 0)
	byHash := make(map[string]domain.Event, len(events))
	for _, event := range events {
		byHash[event.Hash] = event
	}

	for i, event := range events {
		phase := ""
		if event.Phase != nil {
			phase = *event.Phase
		}
		attemptID := ""
		if event.AttemptID != nil {
			attemptID = event.AttemptID.String()
		}
		recomputed, err := domain.EventHash(
			event.PrevHash, event.RunID.String(), event.Type, event.Actor,
			phase, attemptID, formatTS(event.TS), event.Payload,
		)
		if err != nil {
			problems = append(problems, Problem{
				Kind: ProblemChain, Seq: event.Seq,
				Detail: fmt.Sprintf("event payload is not canonicalisable: %v", err),
			})
			continue
		}
		if recomputed != event.Hash {
			problems = append(problems, Problem{
				Kind: ProblemChain, Seq: event.Seq,
				Detail: fmt.Sprintf("hash mismatch: recorded %s, recomputed %s", event.Hash, recomputed),
			})
		}

		if i == 0 {
			if event.PrevHash != "" {
				problems = append(problems, Problem{
					Kind: ProblemChain, Seq: event.Seq,
					Detail: fmt.Sprintf("first event has prev_hash %s, want empty", event.PrevHash),
				})
			}
			continue
		}

		previous := events[i-1]
		if event.Seq <= previous.Seq {
			problems = append(problems, Problem{
				Kind: ProblemChain, Seq: event.Seq,
				Detail: fmt.Sprintf("seq is not increasing: %d follows %d", event.Seq, previous.Seq),
			})
		}
		if event.PrevHash != previous.Hash {
			detail := fmt.Sprintf("prev_hash %s does not chain to seq %d (%s)", event.PrevHash, previous.Seq, previous.Hash)
			if _, survives := byHash[event.PrevHash]; survives {
				detail = fmt.Sprintf("prev_hash %s chains to an event that is out of order; a seq gap suggests a deleted event", event.PrevHash)
			}
			problems = append(problems, Problem{Kind: ProblemChain, Seq: event.Seq, Detail: detail})
		}
	}
	return problems
}

// VerifyArtifacts checks every recorded fingerprint against the bytes on disk.
// A row without a file is an integrity failure, never a cue to silently re-run
// the work that produced it (contracts/store.md §5 step 3).
func VerifyArtifacts(ctx context.Context, refs []domain.ArtifactRef, verifier ArtifactVerifier) []Problem {
	problems := make([]Problem, 0)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			problems = append(problems, Problem{
				Kind: ProblemChain, SHA256: ref.SHA256,
				Detail: fmt.Sprintf("verification cancelled: %v", err),
			})
			return problems
		}
		err := verifier.Verify(ctx, ref.SHA256)
		switch {
		case err == nil:
			continue
		case errors.Is(err, domain.ErrNotFound):
			problems = append(problems, Problem{
				Kind: ProblemMissingFile, SHA256: ref.SHA256,
				Detail: fmt.Sprintf("artifact file %s is missing; expected digest %s", ref.RelativePath, ref.SHA256),
			})
		case errors.Is(err, domain.ErrIntegrity):
			problems = append(problems, Problem{
				Kind: ProblemDigestMismatch, SHA256: ref.SHA256,
				Detail: fmt.Sprintf("artifact file %s does not match its recorded digest %s: %v",
					ref.RelativePath, ref.SHA256, err),
			})
		default:
			problems = append(problems, Problem{
				Kind: ProblemDigestMismatch, SHA256: ref.SHA256,
				Detail: fmt.Sprintf("artifact %s could not be verified: %v", ref.SHA256, err),
			})
		}
	}
	return problems
}

// formatTS renders a timestamp exactly as the writer does, so a recomputed hash
// matches the stored one. Verification would otherwise report every event as
// tampered merely because two layouts disagree.
func formatTS(ts time.Time) string { return domain.FormatTimestamp(ts) }
