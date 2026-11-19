---
title: Error classes and exit codes
type: reference
sources: [S001, S009]
updated: 2026-11-19
---

Every error the framework returns carries a **class**, so retry and exit-code decisions never parse
message strings (S001). Ten classes exist: `transient`, `rate_limited`, `auth`, `permanent`,
`context_overflow`, `invalid_response`, `timeout`, `budget_exhausted`, `cancelled`, `internal`
(S001). `ClassOf(err)` reads it; `HintOf(err)` returns the operator-facing hint that configuration
and integrity failures always carry, naming the setting or file to fix (S001, S009).

Classification is not cosmetic: the retry policy keys off the class, and the process exit code keys
off the classified failure (S001, S009).

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Completed; run terminal or status reported |
| `1` | General failure before or during execution |
| `2` | Usage error (unknown flag, missing argument) |
| `3` | Run reached `failed` |
| `4` | Run is owned by another live process |
| `5` | Definition or configuration invalid — including a resume whose supplied definition drifted |
| `6` | Persistence integrity failure, or state too new to migrate |
| `7` | Cancelled by signal; run left `paused` and resumable |

Exit codes are part of the CLI contract and the only surface the integration tests assert against;
human output is stable prose but not a machine contract (S009). A second signal exits `1`
immediately and leaves the run resumable either way (S009).
