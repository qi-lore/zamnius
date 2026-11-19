---
title: Attempt atomicity and single-host ownership
type: decision
sources: [S001, S003, S006, S007]
updated: 2026-11-19
---

Two rules carry the project's top principle — recoverability — and both were chosen deliberately in
the 2026-11-11 grill rather than discovered later.

## One attempt is one atomic commit

A step attempt commits its state transition, its artifacts, its event records, and its decision
records in a single transaction (S006). The alternative — committing the phase and recording
attempts as log lines — was rejected because a crash between two writes leaves a run that cannot be
reconciled without guessing.

Consequences: an attempt that was interrupted before its commit is restarted from the phase's
*recorded inputs* with the restart cause recorded; side effects carry idempotency keys derived from
run + phase + attempt, so a repeat is detectable rather than silently duplicated (S001, S006).

## Ownership is a lease, on one host

Only one live process may advance a run. Ownership is a time-bounded conditional write on the store,
not a convention; a stale owner's lease can be taken over and the takeover is recorded as a decision
(S007). Exactly one contender wins, and the losers are refused without touching the run's state
(S001).

Cross-host ownership is explicitly **deferred** — the current guarantee is between processes on one
host sharing the store (S001, S007). Treating the lease as a cluster lock would be a misreading.

Both rules exist to make resume boring: a fresh process reads the store, sees which attempts
committed, and continues. The recoverability suite exercises this with a real `SIGKILL` child at four
commit boundaries (S001, S003).
