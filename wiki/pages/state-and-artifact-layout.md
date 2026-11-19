---
title: State and artifact layout on disk
type: component
sources: [S002, S003, S004, S009, S010]
updated: 2026-11-19
---

Everything a run persists lives under one **state directory**, `./.zamnius` by default, overridable in
precedence order by `--state-dir`, `ZAMNIUS_STATE_DIR`, a config file's `state_dir`, then the default
(S010, S009).

```text
.zamnius/
  zamnius.db                          run rows, phases, attempts, events, decisions, artifacts, leases
  artifacts/<2 hex>/<sha256>          content-addressed artifact files
```

Two stores, deliberately (S003, S002):

- **Transactional store** — SQLite, holding run state, checkpoints, leases, and the append-only event
  log. The state transition and its events commit in one transaction, so they cannot drift apart.
- **Content-addressed files** — artifacts are written by fingerprint, durably, *before* their row is
  inserted. `Put` is idempotent by content hash; `Get` verifies the fingerprint before returning
  bytes (S004).

Consequences worth knowing before touching the code:

- A crash between the file rename and the row insert can leave a file with no row; an orphan artifact
  sweep reclaims those, and refuses to run while any run is running (S004).
- Resume verifies every artifact row against its file before reusing it, so a corrupted or missing
  artifact fails the run with evidence instead of being silently reused (S004).
- The persisted format is versioned (`schema 1`); state written by an incompatible version is
  migrated with a recorded migration or refused, never reinterpreted (`cli.md` exit `6`) (S004, S009).

SQLite uses immediate transactions to keep the commit protocol honest (S003).
