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
	"github.com/prompt-fu/zamnius/internal/domain"
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

// ChainVerifier verifies an event chain as it arrives, one event at a time. It is
// the streaming recomputation research.md R12 asks for: whether one event is intact
// follows from that event and its predecessor's digest, so the verifier holds a
// fixed amount of state — the previous digest, the previous seq, and the running
// count — and never the whole log. A log longer than memory therefore verifies in a
// single bounded pass, and there is exactly one implementation of the chain rule, so
// the writer's verifier and the reader's cannot drift; the alternative, a set of
// every digest, would cost O(n) memory to sharpen one message.
type ChainVerifier struct {
	prevHash string
	prevSeq  int64
	count    int
	problems []Problem
}

// NewChainVerifier starts an empty verifier. Events must be added in seq order.
func NewChainVerifier() *ChainVerifier { return &ChainVerifier{} }

// Add checks one event against the chain so far and records any problem with it. It
// recomputes the event's hash over its own recorded prev_hash, so a rewritten payload,
// hash, or prev_hash all surface as a chain problem at that seq rather than as a
// silently shorter log.
func (v *ChainVerifier) Add(event domain.Event) {
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
	switch {
	case err != nil:
		v.problems = append(v.problems, Problem{
			Kind: ProblemChain, Seq: event.Seq,
			Detail: fmt.Sprintf("event payload is not canonicalisable: %v", err),
		})
	case recomputed != event.Hash:
		v.problems = append(v.problems, Problem{
			Kind: ProblemChain, Seq: event.Seq,
			Detail: fmt.Sprintf("hash mismatch: recorded %s, recomputed %s", event.Hash, recomputed),
		})
	}

	if v.count == 0 {
		if event.PrevHash != "" {
			v.problems = append(v.problems, Problem{
				Kind: ProblemChain, Seq: event.Seq,
				Detail: fmt.Sprintf("first event has prev_hash %s, want empty", event.PrevHash),
			})
		}
	} else {
		if event.Seq <= v.prevSeq {
			v.problems = append(v.problems, Problem{
				Kind: ProblemChain, Seq: event.Seq,
				Detail: fmt.Sprintf("seq is not increasing: %d follows %d", event.Seq, v.prevSeq),
			})
		}
		if event.PrevHash != v.prevHash {
			detail := fmt.Sprintf("prev_hash %s does not chain to seq %d (%s)", event.PrevHash, v.prevSeq, v.prevHash)
			if event.Seq > v.prevSeq+1 {
				detail = fmt.Sprintf(
					"prev_hash %s does not chain to seq %d (%s); the seq gap between %d and %d suggests a deleted event",
					event.PrevHash, v.prevSeq, v.prevHash, v.prevSeq, event.Seq)
			}
			v.problems = append(v.problems, Problem{Kind: ProblemChain, Seq: event.Seq, Detail: detail})
		}
	}

	v.prevHash = event.Hash
	v.prevSeq = event.Seq
	v.count++
}

// Count is how many events the verifier has folded in.
func (v *ChainVerifier) Count() int { return v.count }

// Problems returns the chain problems found so far, in the order the events arrived.
func (v *ChainVerifier) Problems() []Problem { return v.problems }

// VerifyChain recomputes every event hash and checks the links between events by
// folding the log through a ChainVerifier, one event at a time (R12). A deleted event
// shows up as a seq gap or as a prev_hash that points at a digest no predecessor
// carries; both are reported, because silently accepting either would make the record
// unfalsifiable.
func VerifyChain(events []domain.Event) []Problem {
	verifier := NewChainVerifier()
	for i := range events {
		verifier.Add(events[i])
	}
	return verifier.Problems()
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
