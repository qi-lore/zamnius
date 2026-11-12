package domain

import (
	"encoding/json"
	"fmt"
)

// eventEnvelope is the exact byte shape an event hash is computed over. Adding a
// field here changes every future hash, so it is a format decision, not a detail.
type eventEnvelope struct {
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Phase     string          `json:"phase"`
	AttemptID string          `json:"attempt_id"`
	TS        string          `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// EventHash recomputes an event's chained digest from its durable columns. It
// lives with the canonical form on purpose: the writer and the verifier must run
// exactly one implementation of the chain rule, or a tamper check would compare
// two different definitions of integrity.
func EventHash(prevHash, runID, eventType, actor, phase, attemptID, ts string, payload []byte) (string, error) {
	envelope := eventEnvelope{
		RunID:     runID,
		Type:      eventType,
		Actor:     actor,
		Phase:     phase,
		AttemptID: attemptID,
		TS:        ts,
		Payload:   json.RawMessage(payload),
	}
	canonical, err := CanonicalJSON(envelope)
	if err != nil {
		return "", fmt.Errorf("canonicalise event: %w", err)
	}
	return ChainHash(prevHash, canonical), nil
}
