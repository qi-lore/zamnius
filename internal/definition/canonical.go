package definition

import (
	"fmt"

	"github.com/prompt-fu/zamnius/internal/domain"
)

// Snapshot returns the canonical JSON of a normalized definition. It is stored on
// the run and must round-trip exactly: the fingerprint is computed over these
// bytes, so a resume can prove it is executing the same workflow.
func Snapshot(def Definition) ([]byte, error) {
	snapshot, err := domain.CanonicalJSON(def)
	if err != nil {
		return nil, fmt.Errorf("snapshot definition: %w", err)
	}
	return snapshot, nil
}

// Fingerprint is the sha256 of the canonical snapshot. It detects definition
// drift at resume (data-model.md §10).
func Fingerprint(def Definition) (string, error) {
	snapshot, err := Snapshot(def)
	if err != nil {
		return "", err
	}
	return domain.HashBytes(snapshot), nil
}

// InputFingerprint identifies the exact inputs of one attempt: the normalized
// phase, its resolved agent, and the upstream artifact digests. Attempt atomicity
// relies on it, so it is computed from data, never from time or identifiers.
func InputFingerprint(def Definition, phaseIndex int, upstream []string) (string, error) {
	if phaseIndex < 0 || phaseIndex >= len(def.Phases) {
		return "", fmt.Errorf("input fingerprint: phase index %d out of range", phaseIndex)
	}
	payload := map[string]any{
		"phase":    def.Phases[phaseIndex],
		"ordinal":  phaseIndex,
		"upstream": upstream,
	}
	encoded, err := domain.CanonicalJSON(payload)
	if err != nil {
		return "", fmt.Errorf("input fingerprint: %w", err)
	}
	return domain.HashBytes(encoded), nil
}

// PromptVersion identifies the prompt an attempt used. Instructions are inline in
// this increment, so their digest is their version: a changed instruction is a
// different version, and a recorded attempt stays traceable to the text it used
// (FR-039).
func PromptVersion(instructions string) string {
	return "inline:sha256:" + domain.HashBytes([]byte(instructions))
}
