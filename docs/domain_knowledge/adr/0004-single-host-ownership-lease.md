# ADR-0004: Single-host run ownership lease

**Status**: accepted · 2026-11-11

## Context

Two instances must never advance the same run, and a crashed owner's run must become resumable. Enforcement could be local to a process, scoped to one host sharing a store, or distributed across hosts with consensus.

## Decision

Ownership is a time-bounded lease enforced between processes on one host that share the durable store. An expired lease is reclaimable, and every takeover is recorded. Cross-host coordination is out of scope for this increment.

## Consequences

- Exactly-one-owner and stale-takeover are testable without distributed-systems machinery.
- Running the same store from two hosts is unsupported and must be documented as such.
- The lease interface is designed so a later increment can add cross-host fencing without changing workflow definitions.
