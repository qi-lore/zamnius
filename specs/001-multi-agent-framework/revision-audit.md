# Revision audit — `specs/001-multi-agent-framework` against the shipped increment

**Date**: 2026-11-19 · **Base**: `dev` (v0.1.0 tree plus the knowledge-layer and configuration work,
merge `c732ad2`, and PR #11 open at the time of writing) · **Author**: build agent, by direct
inspection

This is a point-in-time revision audit: it compares the feature artifacts of increment 1 — spec, plan,
data model, contracts, quickstart — with the code that shipped as v0.1.0. It changes no behaviour. Every
finding is either **fixed here**, **reported for a decision**, or **recorded as accepted drift**; a
finding that needs a decision is priced, not silently implemented.

The rule this audit follows: an artifact is the record, so a claim that no longer describes the code is
a defect in the artifact, while a behaviour that no artifact promises is a defect in the spec
(AGENTS.md agreement 6, ADR-0008).

---

## 1. Method

Mechanical where possible, so the results are reproducible from the commands quoted with each check:

| Check | Command (abridged) |
|---|---|
| FR / SC citations resolve | `grep -rhoE 'FR-[0-9]{3}'` over code, tests, and docs vs. the set defined in `spec.md` |
| Public surface size | AST walk over `pkg/zamnius` counting exported package-level declarations |
| Store schema vs `store.md` §3 | table, index, trigger, and pragma names extracted from `internal/store/sqlite` |
| Exit codes vs `cli.md` §3 | `internal/cli` constants and the mapping function in `cli.go` |
| Environment variables vs `cli.md` §4 | `grep -rhoE 'ZAMNIUS_[A-Z_]+'` over `internal/`, `cmd/` |
| Subcommands vs `cli.md` §1 | the usage string and the dispatch in `internal/cli/cli.go` |
| Documented paths exist | every `test/…`, `examples/…`, `specs/…` path mentioned in `quickstart.md` and `README.md` |
| Adapter claims vs `model-client.md` §2–§3 | greps for `response_format`, `strict`, `finish_reason`, `usage`, timeout, retry |
| Commits are bisectable | each code commit of PR #11 checked out in a throwaway worktree, `go build` + `go vet` + package tests |

**What this audit did not do**: it did not re-run the 100-run fault-injection corpus of SC-001, nor time
SC-013's fifteen-minute onboarding claim, nor audit the deferred-capability requirements beyond
classifying them as deferred (§4, finding A7). Those need their own runs, not a reading.

---

## 2. What holds

Recorded because an audit that only lists problems is not evidence of anything:

- **No dangling citations.** All 27 `FR-` identifiers cited outside `specs/` (25 of them from code
  or tests, two more from docs) and all 9 cited `SC-` identifiers exist in `spec.md`; 63 FRs and 16
  SCs are defined.
- **The public contract is the size the changelog claims.** 98 exported package-level identifiers
  (3 functions, 46 types, 49 constants and variables) — exactly the figure T065 recorded.
- **The store matches `contracts/store.md` §3** table for table (`runs`, `phases`, `attempts`,
  `artifacts`, `events`, `decisions`, `schema_migrations`), including the two append-only triggers, the
  `CHECK ((owner_id IS NULL) = (lease_expires_at IS NULL))` constraint, and the pragmas
  (`journal_mode=WAL`, `synchronous=FULL`, `busy_timeout=5000`, `foreign_keys=ON`, one write
  connection).
- **The CLI matches its contract**: the four documented subcommands and the eight exit codes are the
  four dispatch cases and the eight constants, and every code maps to the class the table names.
- **The environment surface matches** `cli.md` §4 exactly — seven variables read, seven documented
  (the seventh, `ZAMNIUS_LEASE_TTL`, added by PR #11).
- **The reference adapter matches `model-client.md`**: structured output with a single
  `response_format` fallback, required `usage`, `finish_reason` mapping, and no hidden timeout or retry.
- **Documented paths resolve.** Every fixture and directory named by `quickstart.md` and `README.md`
  exists; the one `testdata/workflows/` hit that looks like a miss is the *prose* in §Definition of done
  that records the T068 correction.
- **PR #11's commits are individually green**, so the branch is bisectable.

---

## 3. Findings

### A1 — SC-011 has no parity check, and currently fails for `Verify` (needs a decision)

**Severity**: high, because a success criterion that cannot be met is worse than none.

SC-011: "100% of documented core operations are available on every access surface delivered by this
increment (library and command line), verified by a parity check."

- The library documents five operations (`CHANGELOG.md` §Entry point and operations:
  `Start`, `Resume`, `Status`, `Artifacts`, `Verify`); the CLI delivers four (`run
  start|resume|status|artifacts`).
- `Engine.Verify` (`pkg/zamnius/engine.go:122`), which walks the event chain and every artifact
  fingerprint, is therefore reachable only from Go code. The CLI verifies artifact *fingerprints* as a
  side effect of `run artifacts --out`, and surfaces a broken chain only on `run resume`; there is no
  command that verifies a completed run without resuming it.
- No parity check exists: `grep -rn "parity" test/` finds only the definition-format parity tests,
  which are unrelated.

**Options**: (a) add `zamnius run verify <run-id>` — small, and it is the operation an operator
reaching for "is this state trustworthy?" wants without resuming; or (b) clarify SC-011 to the surface
the spec's own Clarification already names (start, resume, status, list artifacts) and state that
verification is library-only in this increment. Recommendation: **(a)**, with (b) as the fallback if
the follow-on increment is where CLI verification belongs.

### A2 — FR-022's traces and metrics are unimplemented and unclarified (needs a decision)

**Severity**: medium. The software is honest about it; the spec is not.

FR-022: "The system MUST emit structured logs, traces, and metrics suitable for production diagnosis."

- Structured logs: shipped (`log/slog`, levels and formats configurable).
- Traces: `internal/observability.Tracer` and `NoopTracer` exist, but **nothing calls them** —
  `grep -rn "Tracer"` outside `internal/observability` returns nothing, so no span is ever started.
- Metrics: **nothing anywhere** — no metrics machinery exists in `internal/`, `pkg/`, or `cmd/` (the
  only textual match for "metric" is the word *asymmetric* in a comment).
- `research.md` R8 records the intent ("the interface landed with a no-op default and **no** OTel
  import"), but `spec.md` carries no Clarification for it and no SC covers observability, so a reader
  of `spec.md` alone concludes FR-022 is met.

**Recommendation**: a dated Clarification — *increment 1 emits structured logs; tracing is a seam with
no implementation and metrics are out of scope until an exporter is added* — so the deviation is a
recorded decision rather than an unmet MUST.

### A3 — `Session` is canonical vocabulary and a listed entity, but has no model and no persistence

**Severity**: medium — it is the finding most likely to mislead the next author.

- `CONTEXT.md` (canonical vocabulary) defines **Session** as "the context thread of exactly one agent
  instance within a run; holds that instance's short-term memory and its own resume point"; `spec.md`
  lists it among the key entities and its Clarifications fix the hierarchy as *run → phase → session →
  step attempt*.
- The shipped increment has no session anywhere: no `Session` type in `internal/domain`, no session
  column or table in the store, and no session in `data-model.md` — whose relationship summary is
  `Run 1─* PhaseState 1─* Attempt 1─* Artifact`, i.e. the hierarchy stops at the attempt.

Increment 1's actual model is that a phase (or a branch) runs exactly one agent instance, so nothing
needs a session identity yet. That is defensible; leaving the canonical vocabulary silent about it is
not, because `CONTEXT.md` is the file a 002 author reads first.

**Recommendation**: a dated Clarification — *increment 1 has no session entity; an agent instance is
the unit a phase or branch runs, and attempts are keyed by run, phase, and attempt number; the session
vocabulary belongs to the follow-on increment where sessions resume independently* — plus one line in
`CONTEXT.md` marking the term as follow-on.

### A4 — the exit-`4` lease refusal still double-wraps its message (open defect, small fix)

**Severity**: low (cosmetic, hint is correct) but it was already recorded as a defect in T068's ledger
and is still true.

`internal/store/sqlite/lease.go:61–62` wraps the sentinel with context, `domain.Error.Error()` prefixes
the class, and `internal/cli/resume.go` (`ownerConflict`) copies the whole string into `Msg`. The
operator therefore reads

```text
permanent: acquire lease for <id> as <owner>: permanent: run is owned by another live process: …
```

The actionable hint (owner and expiry) is unaffected, and `test/integration/resume_test.go:156` asserts
only the exit code, which is why nothing catches it.

**Fix**: give the conflict a fixed `Msg` ("run is owned by another live process") and keep the wrapped
error in `Err:` for `errors.Is`; add an assertion on the message so it cannot regress.

### A5 — `plan.md`'s structure listing has fallen behind the tree (low, documentation freshness)

`plan.md` §Project Structure predates `internal/strict` (added by PR #11) and the new files in
`internal/domain` (`env.go`, `defaults.go`). The section is explicitly a Phase-1 statement of intent, so
this is not a contract breach — but `AGENTS.md` treats a stale structure line the way it treats a stale
`Current state`, and the same applies here. **Fix**: refresh the listing when increment 2's plan is
written, or mark the section point-in-time as `plan.md` already does for its handoff snapshot.

### A6 — `library-api.md` documented an `Options.Tracer` field that never existed (fixed in PR #11)

Found by this audit: the contract listed `Tracer trace.Tracer` among `Options`, while the shipped
struct has no such field (`pkg/zamnius/engine.go`). The tracer seam is `internal/observability.Tracer`
and is not part of the public options. PR #11 corrects the listing to the shipped struct and adds
`LeaseTTL`, which the code *does* accept. Recorded here so the correction is traceable to the finding
that prompted it.

### A7 — most FRs are cited nowhere outside `spec.md` (traceability, mostly expected)

36 of 63 FRs appear in no code, test, or document outside `specs/`; 25 are cited by code or tests, and
2 more only by docs. Classified:

- **Most are deferred-capability FRs** (roles, task board, messaging, memory, prompts, tools/MCP,
  workspaces, HTTP/gRPC/A2A/AG-UI — FR-024 through FR-050 and FR-053). Their headings already say
  "follow-on increment", so their absence from the code is scope, not drift.
- **Seven in-scope FRs are implemented but unnamed by any test**: FR-010, FR-019, FR-020, FR-021
  (actor/phase/time/outcome/cause on every transition; append-only total order; decision records;
  immutability — all exercised by `test/audit`'s chain, completeness, and reconstruction suites),
  FR-022 (finding A2), FR-058 (the provider-neutral contract, documented in `contracts/model-client.md`),
  and FR-062 (agent identity: name, instructions, model selection, budgets — enforced in
  `internal/definition`).

**Fix**: cheap and optional; cite the FRs in the tests that already cover them, or accept the gap
explicitly. The behaviour is not in doubt — the traceability chain is.

### A8 — one spec heading is ambiguous about what this increment delivers (low, clarity)

`#### Access surfaces and configuration (story 7; this increment delivers FR-060 only)` sits above
FR-050–FR-053 *and* FR-054–FR-057, of which FR-051, FR-052, FR-054, and FR-056 are delivered by
increment 1. The parenthetical is true only of the access-surface requirements. **Fix**: reword to name
the delivered set (FR-051, FR-052, FR-060, and the security requirements) and leave HTTP, gRPC, network
resilience, and backend selection to the follow-on increment.

---

## 4. Disposition

| Finding | Disposition | Where the change belongs |
|---|---|---|
| A1 SC-011 parity / `Verify` on the CLI | **Decision needed** — add `run verify`, or scope SC-011 | `internal/cli` + `contracts/cli.md`, or `spec.md` Clarifications |
| A2 FR-022 traces and metrics | **Decision needed** — dated Clarification | `spec.md` → `### Session 2026-11-19` |
| A3 `Session` vocabulary vs no session entity | **Decision needed** — dated Clarification + `CONTEXT.md` line | `spec.md`, `docs/domain_knowledge/CONTEXT.md` |
| A4 exit-`4` message double-wrap | Open defect, small fix with a test | `internal/cli/resume.go`, `internal/store/sqlite/lease.go` |
| A5 `plan.md` structure listing | Deferred to increment 2's plan, or mark point-in-time | `plan.md` |
| A6 `Options.Tracer` | **Fixed** in PR #11 | `contracts/library-api.md` |
| A7 FR traceability | Optional; accepted gap unless a reviewer wants the citations | tests, nothing else |
| A8 ambiguous spec heading | Reword | `spec.md` |

Findings A1–A3 and A8 are *spec-level* decisions: they belong in `spec.md` under a new dated
Clarifications session, which is a change to the specification and therefore needs the reviewer's
acceptance rather than this audit's assumption. A4 is a code fix. A5 and A7 are hygiene.

Nothing in this audit weakens a recoverability or integrity promise; every finding is about a claim
that is broader than the code, a message that repeats itself, or a listing that aged.
