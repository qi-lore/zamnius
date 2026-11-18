package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalJSON encodes v deterministically: encoding/json sorts map keys, and
// HTML escaping is disabled so "<" is not rewritten as "\u003c". Every hash in
// this project is computed over this encoding, so a change here invalidates all
// stored fingerprints — treat it as a format change.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	// Encode appends a newline; dropping it keeps the bytes stable across callers
	// that would otherwise chain hashes of differently terminated input.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	// Copy so the caller's slice does not alias encoder internals.
	return append([]byte(nil), out...), nil
}

// HashBytes returns the lowercase hex sha256 of b. It is the single place the
// project computes content fingerprints (artifacts) and event hashes.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ChainHash links an event to its predecessor: sha256(prev_hash || payload).
// The first event of a run has an empty prev_hash, which keeps the rule uniform.
func ChainHash(prevHash string, payload []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(prevHash))
	_, _ = h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyDigest compares content against a recorded sha256 without leaking which
// half differed, and without panicking on a malformed digest.
func VerifyDigest(recorded string, content []byte) bool {
	if len(recorded) != 64 {
		return false
	}
	return HashBytes(content) == recorded
}
