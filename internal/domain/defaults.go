package domain

import "time"

// Execution defaults that more than one layer must agree on. They live here
// because they are properties of a run's behaviour, not of an adapter: the engine
// enforces them and the process-settings surface defaults to the same values, so a
// single definition is the only way they cannot drift (ADR-0010).
const (
	// DefaultMaxConcurrency caps parallel branches in a fan-out phase (FR-004).
	DefaultMaxConcurrency = 8
	// DefaultLeaseTTL is the ownership window; renewal happens at TTL/3 (T048).
	DefaultLeaseTTL = 30 * time.Second
)
