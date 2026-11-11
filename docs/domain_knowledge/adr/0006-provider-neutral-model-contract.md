# ADR-0006: Provider-neutral model contract with one reference adapter

**Status**: accepted · 2026-11-11

## Context

The framework must not be tightly coupled to a model provider, yet agents must actually execute and token budgets must be enforceable. Provider-specific code could live in an adapter, in the agent loop, or outside the framework entirely.

## Decision

The core defines a provider-neutral model contract covering prompt input, structured result, token usage, cancellation, and error classification. Provider-specific behaviour stays behind it. The increment ships at least one working reference adapter, and tests use deterministic fakes so recoverability tests run offline.

## Consequences

- The framework can be adopted without changing model vendors, and token accounting is uniform.
- The reference adapter is a real maintenance commitment and a place where provider drift will show up first.
- Provider-specific continuation state must never be persisted, because a resumed run may use a different provider.
