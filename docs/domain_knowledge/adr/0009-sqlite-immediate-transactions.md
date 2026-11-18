# ADR-0009: SQLite transactions begin immediately, so lease contention serialises

**Status**: accepted · 2026-11-12

## Context

[ADR-0004](0004-single-host-ownership-lease.md) enforces run ownership between processes that share one durable
store, and it rests on a single fact: `AcquireLease` decides the winner with one conditional `UPDATE`, so two
processes cannot both believe they own a run. The loser must learn it lost — `ErrRunAlreadyOwned` — because the
distinction between "someone else owns this run" and "the database failed" drives whether the caller waits for the
lease to lapse or aborts.

The store opened its transactions in SQLite's default **deferred** mode. A deferred transaction takes no lock at
`BEGIN`: it reads, then upgrades to a write when the first `UPDATE` runs. In WAL mode that upgrade is only safe if
no other connection has committed since the read. When one has, SQLite refuses the upgrade with
`SQLITE_BUSY_SNAPSHOT` **immediately** — and, deliberately, does not consult `busy_timeout`, because waiting cannot
make a stale snapshot fresh. Two processes contending for one lease therefore produced a raw
`database is locked (5) (SQLITE_BUSY)` instead of `ErrRunAlreadyOwned`.

This went unnoticed because every existing test exercised contention either within one store — which holds a single
connection (`SetMaxOpenConns(1)`), so writes serialise in the connection pool — or across two stores *sequentially*.
Neither is the case the lease exists for. The two-process contention test written for T045 exposed it.

## Decision

Every transaction the store begins is **immediate**: the DSN sets `_txlock=immediate`, so `BeginTx` issues
`BEGIN IMMEDIATE` and takes the write lock up front. Contenders then serialise through `busy_timeout(5000)` — the
busy handler *is* consulted when the lock cannot be taken at `BEGIN` — and when a contender finally runs its
conditional `UPDATE`, it sees the committed lease and is correctly told the run is owned.

Read paths (`LoadRun`, `Status`, inspections) use autocommit queries and no explicit transaction, so they are
unaffected and still take no write lock.

## Consequences

- Lease contention across processes yields `ErrRunAlreadyOwned`, restoring the guarantee ADR-0004 was written to
  provide; a stored database lock is no longer mistaken for an ownership decision.
- Pinned by `TestConcurrentAcquireLeaseAcrossConnectionsHasOneWinner` (`internal/store/sqlite/lease_test.go`), which
  fails on the deferred DSN with `SQLITE_BUSY` and passes immediate.
- Writers acquire the write lock at `BEGIN` rather than at their first `UPDATE`. For this store that is the intended
  shape anyway — one writer connection, and every state change commits with its events (FR-011) — so the cost is
  that a transaction which would have read then aborted now briefly holds the lock.
- Does not weaken ADR-0003 or ADR-0004: attempt atomicity and the single-host lease are unchanged in meaning; only
  the transaction mode that lets them hold under contention is made explicit.
- Any future store backend must preserve the same property: a contender must observe the committed lease and be
  told it lost, not handed a transport-level error.
