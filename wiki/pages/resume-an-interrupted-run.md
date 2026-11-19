---
title: Resume an interrupted run
type: howto
sources: [S001, S009, S010, S013]
updated: 2026-11-19
---

Resuming is the product's headline capability, so the procedure is worth knowing exactly.

## Normal interruption

`SIGINT`/`SIGTERM` cancels the run's context; the engine commits the attempt boundary, releases the
lease, marks the run `paused`, and exits `7` (S009). Nothing else is required — the run is already
resumable.

```console
$ zamnius run start --file slow.yaml
^C
$ echo $?        # 7
```

## After a hard kill

A `kill -9` leaves the lease behind; it expires, and the next attempt to resume either waits or takes
over the stale lease with a recorded takeover decision (S001, S007).

```console
$ zamnius run status <run-id>     # paused/running, last five events, next action
$ zamnius run resume <run-id>
```

## What resume refuses, and why

| Symptom | Exit | Meaning |
|---|---|---|
| Owner named in the message | `4` | Another live process holds the lease; wait for expiry |
| Names both definition fingerprints | `5` | The file changed since the run started (drift); pass `--allow-definition-drift` deliberately |
| Fingerprint or chain mismatch | `6` | Persisted state disagrees with itself; verify before trusting |

Resume never re-executes committed work and never repeats a recorded side effect; a terminal run is a
no-op that exits `0` (S001, S009). Every artifact row is verified against its file before reuse, so a
corrupt artifact fails the run with evidence instead of being quietly reused (S001).

For the drill with a real `SIGKILL`, use the delay-heavy fixture and wait out the lease window
(S010, S013).
