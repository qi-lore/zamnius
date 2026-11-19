# Zamnius — agent guide

Read this before touching the repository. It is the short entry point; the spec, plan, and
constitution are the authoritative sources.

## What this is

Zamnius is a recoverable multi-agent workflow framework in Go. Module path
`github.com/qi-lore/zamnius`. The committed increment covers **only** ordered phases with
fan-out/join, resumability, and audit reconstruction (stories P1–P3). Roles, task board, messaging,
memory, tools/MCP, HTTP, gRPC, A2A, and AG-UI are deliberately deferred to later feature specs.

## Where the truth lives

```text
constitution  .specify/memory/constitution.md        non-negotiable principles
spec          specs/001-multi-agent-framework/spec.md       what and why, with Clarifications
plan          specs/001-multi-agent-framework/plan.md       how, structure, gates, risks
research      specs/001-multi-agent-framework/research.md   R1–R14 decisions + alternatives
data model    specs/001-multi-agent-framework/data-model.md entities, states, invariants
contracts     specs/001-multi-agent-framework/contracts/    schemas and interfaces
validation    specs/001-multi-agent-framework/quickstart.md runnable scenarios per story
glossary      docs/domain_knowledge/CONTEXT.md              canonical vocabulary
decisions     docs/domain_knowledge/adr/0001–0009           architectural rationale
```

Chat history is never the source of truth. A fresh agent must be able to resume from these files
alone (constitution Principle II).

## Current state (2026-11-19)

- Spec is grilled and committed; 12 decisions recorded in `spec.md` → `## Clarifications`.
- Plan, research, data model, contracts, quickstart, and **`tasks.md`** are committed.
- `tasks.md` holds **68 TDD tasks in six phases**: Setup T001–T005, Foundational T006–T019
  (blocks every story), US1 T020–T042, US2 T043–T053, US3 T054–T062, Polish T063–T068.
- **Implementation is well under way: T001–T062 are done and tested, so US1, US2, and US3 are
  complete** — the recoverability proofs (four commit-boundary interruption points, a genuine
  `SIGKILL`, ownership/lease takeover, attempt reconciliation, lease heartbeat, definition-drift
  refusal, schema-version migration/refusal, the `run resume` command, signal handling, and the
  paused position) are all in place and committed, and US3's audit suite
  (`test/audit/{chain,artifact,completeness,reconstruct,views}_test.go` over the shared
  `test/audit/harness_test.go`, selected by `go test ./test/audit -run TestVerify`) is green: chain
  integrity — recomputed event by event through a streaming `audit.ChainVerifier` — artifact
  integrity, an eight-run completeness corpus covering every decision kind, a
  22-run reconstruction corpus over `internal/audit/view.go` (a reader handed only the store states
  the position, completed and pending work, and the next action), and the remaining views — the
  ordered timeline with decisions interleaved at their commit, the run status view (`Status`/
  `StatusView`, which `Engine.Status` now returns by alias), and the verification view (`Verify`,
  which `Engine.Verify` delegates to). T055 also
  closed a contract gap: resume now verifies every artifact row against its file before
  reconciliation, so a corrupted or missing artifact fails the run with evidence instead of being
  silently reused (store.md §5 step 3). T059 closed the companion gap: resume's `failOnIntegrity`
  path on a broken chain now has its dedicated test. T060 closed the last US3 store gap: the orphan
  artifact sweep (`internal/store/artifactfs/sweep.go`) reclaims the files a crash between rename and
  row can leave, refusing while any run is running. T061 closed the integrity-facing CLI: `run
  artifacts --out <dir>` (`internal/cli/artifacts.go`) verifies each fingerprint before writing and
  aborts with exit `6` on a mismatch, and `run status` (`internal/cli/status.go`) prints the last five
  events in both forms. T062 locked the audit surface end to end: `test/integration/cli_test.go` now
  asserts the enriched status view, export verification of every fingerprint, and exit `6` on a
  corrupted artifact file through the same contract harness as the rest of the CLI. **US3 is complete
  and Polish has begun: T063 is done** — `test/secrets/secrets_test.go` (new package `test/secrets`,
  no production change) runs the offline corpus with `ZAMNIUS_MODEL_API_KEY=sk-test-canary` in a real
  child process (the composition root binds the logger to `os.Stderr`, so only a process boundary
  captures the shipped log lines) and asserts zero findings for the canary and for any `Authorization`
  value across the captured envelopes and logs, a byte sweep of every file the corpus wrote (artifacts,
  exports, the SQLite store and its WAL), and every event payload, decision, attempt, and artifact row
  read back through the store; an adversarial case proves a provider that echoes the rejected
  credential still cannot push it into an error hint, an event, an attempt row, or a log line. T064
  replaced the placeholder `README.md` with the getting-started guide (install, provider configuration,
  the three formats, a single-phase run, a fan-out run, status and export, interrupt-and-resume, the
  `--json` envelope and exit table, the state directory, the library entry point, and the increment's
  scope); every console block in it was reproduced by running the built binary against a local
  OpenAI-compatible stub, including the resume drill with a real `SIGKILL`. T065 added `CHANGELOG.md`,
  which records the initial public contract set of `pkg/zamnius` (all 98 exported package-level
  identifiers) and states the compatibility policy of `contracts/library-api.md` §5 — additive changes
  are minor, removing or changing an exported signature is major, every public contract change carries
  a changelog note (SC-016), and `internal/` is not part of the contract. T066 added
  `examples/workflows/README.md`, which annotates each of the seven samples with the behaviour it
  demonstrates (the three formats, the `all`-joined fan-out with a dependent phase, the interruption
  drill, the run-level token budget with the budget-stop path, and the schema-refused definition) and is
  linked from `README.md` and `quickstart.md`. T067 measured coverage — `internal/engine` 84.1%,
  `internal/store/sqlite` 80.6%, `internal/audit` 85.5% (all ≥ 80%), whole module 84.0%, no library
  package below 60% — and closed a 79.4% shortfall in `internal/store/sqlite` with two focused tests
  (`TestOpenRequiresAPathAndReportsIt`, `TestLoadRunToleratesALegacyTimestamp`); the measurement is
  recorded in `quickstart.md` §Automated equivalents. T068 ran the full verification sweep — build,
  vet, test, race, the pure-Go build, and `go test ./test/recoverability -count=10` with zero flakes —
  and every quickstart scenario by hand against a local OpenAI-compatible stub, recording the outcome
  in `quickstart.md` §Definition of done; the guide was the defect, not the code, so the scenario
  `--file` paths (`testdata/workflows/…` → `test/testdata/workflows/…`), scenario 1's nonexistent
  `runs/` listing, scenario 2's "exactly one aggregate", scenario 3's fixture (the delay-heavy
  `slow.yaml`, not `fanout.yaml`) and its missing 30s lease wait, scenario 5 (a two-phase workflow,
  because a single phase never reaches the budget), and the Automated-equivalents table's no-op
  `-run` filter were all corrected. **Every task T001–T068 is done; the increment's task list is
  complete.** For the
  exact per-task state — done,
  implemented-but-untested, or untouched — and the design decisions taken along the way, read
  `tasks.md` → `## Implementation status (2026-11-12)`; that section is authoritative until the
  boxes above it are updated.
- **Release and integration.** v0.1.0 is published: `main` @ `fe5572d`, tag `v0.1.0`, GitHub
  pre-release. Integration happens on **`dev`**, which is ahead of `main`: the increment landed through
  PRs #1–#5 and the knowledge and configuration work through #6–#10, each on a short-lived branch with
  one artifact per commit. `main` receives `dev` only when a release is cut. The feature branch
  `001-multi-agent-framework` still exists but is no longer where work happens; new work starts from
  `dev`. Working tree is clean; every commit is local.
- **The knowledge layer is populated (2026-11-19).** `docs/memory/` no longer has empty sections:
  `INDEX.md` routes to the fan-out timing flake (the defect that failed the release PR) and to the rule
  that `go install …@tag` is proved from an empty `GOBIN`, and it states *why* `architecture/` and
  `worklog/` stay empty — boundaries are canonical in `plan.md` and the ADRs, and the worklog is a
  ledger of lessons rather than a changelog. `wiki/` holds six cited pages over fourteen registered
  sources, typed as `wiki/SCHEMA.md` requires. Both layers are **derived views**: they cite
  `spec.md`/ADRs/`CONTEXT.md` and never override them (ADR-0008). They were written by direct edit
  because this repository has no MCP server and no `/speckit.*` commands in a session; agreement 9
  below is what keeps such an edit deliberate rather than incidental.
- **ADR-0010 fixes the two configuration surfaces** (accepted 2026-11-19): a setting belongs to the
  **logic** surface if the run snapshots it, to **config** if it changes only how the process runs, and
  to **the environment** if it is a credential. Its five recorded deviations are closed on
  `fix/0010-config-followups`: `model.base_url` is refused in a definition rather than accepted and
  ignored (the struct field survives only so a snapshot written by v0.1.0 still decodes, in the
  original field order, because the fingerprint is the hash of those bytes); the concurrency default is
  defined once in `domain`; both surfaces decode through `internal/strict`, so a duplicate JSON key is
  refused in a definition too; `lease_ttl` / `ZAMNIUS_LEASE_TTL` is a process setting that feeds
  `Options.LeaseTTL`, while `Owner` stays library-only because takeover evidence must not be
  self-declared. `README.md` and `contracts/cli.md` §4 list the settings; `CHANGELOG.md` records the
  additions and the refusal under `## [Unreleased]`.
- **Ignore files are shared, not branch-local.** `dev` and `main` both derive from the initial
  commit `0a512c3`. On **2026-11-13** `dev` was advanced by the two ignore commits the feature
  branch already carried (`fbde31c`, `0878256`), so both branches now hold **byte-identical**
  `.gitignore` and `.specify/.gitignore`. Because the change is identical on both sides, a
  `feature → dev` merge is conflict-free on those files — verified with
  `git merge-tree --write-tree dev 001-multi-agent-framework`, which returns a clean tree. The
  branches have therefore diverged (the feature branch is no longer a descendant of `dev`), so
  integration is a real 3-way merge. Add an ignore rule in one place and mirror the exact content;
  a divergent edit on a single branch is what re-introduces the conflict — and that is what
  happened: the feature branch added `.specify/assessments/` in `f6f101f` without mirroring it to
  `dev`, which re-broke the `feature → dev` merge. On **2026-11-18** `dev` was re-synced with that
  rule (`479475c`), so the ignore files are byte-identical again and
  `git merge-tree --write-tree dev 001-multi-agent-framework` is clean once more.
- Source now exists: `cmd/zamnius`, `internal/{app,audit,cli,config,definition,domain,engine,fault,model,observability,store}`,
  `pkg/zamnius`, and tests under `test/{audit,fakes,integration,recoverability,secrets,testdata}`. No
  commit may claim behaviour that has no test.
- Verification at this checkpoint is green, in order: `go build ./...`, `go vet ./...`,
  `go test ./... -count=1`, `go test ./... -race -count=1`, `CGO_ENABLED=0 go build ./...`.
- Keep this section current when a phase completes: it is how a fresh session resumes
  (constitution Principle II.1), so a stale line here is a defect, not a cosmetic issue.

## Working agreements

1. **One commit per artifact.** Each spec/plan/contract/glossary/ADR file is committed on its own;
   each code package is its own commit. A source file commits **together with the tests that cover
   it** — never behaviour without its failing-first test.
2. **Conventional Commits, single line only.** `type(scope): summary`. No bodies, no multi-line.
3. **Never push or touch the remote.** Only the `gitt` agent performs remote operations, and only on
   explicit instruction. Commits are local.
4. **No secrets** in artifacts, events, logs, or prompts; secrets come from the environment
   (`ZAMNIUS_MODEL_API_KEY`) and are redacted before persistence.
5. **Never weaken recoverability** for convenience. Attempt-level atomicity (ADR-0003) and the
   single-host lease (ADR-0004) are load-bearing; changes there need a new ADR.
6. **Fix the artifact, not the chat.** If behaviour must change, update spec → plan → tasks → code.
7. **One task at a time, then stop for review.** Work proceeds in `tasks.md` order, one numbered task per turn
   (`T049` — not a phase, not a story, not a batch). When a task is done: run its tests plus `go build ./...`
   and `go vet ./...`, set its checkbox, update `## Implementation status`, report what changed, where, and how
   it was verified, then **STOP** and wait for the reviewer's go-ahead before starting the next task. Silence,
   a resumed session, or your own confidence is never approval. Never bundle several tasks into one turn or one
   commit — a human has to be able to review each change (constitution Principle XI; rationale in ADR-0007).
8. **Shared ignore files.** `.gitignore` and `.specify/.gitignore` are held **byte-identically** by
   `dev` and every feature branch so integration never conflicts on them. Add a rule in one place
   and mirror the exact content across branches; never edit an ignore file on a single branch.
   Machine-local noise (Spec Kit catalog caches, agent work plans, local run state) is already
   covered — run `git check-ignore -v <path>` before adding anything.
9. **Local-only zones are not backups.** `.specify/assessments/`, `notes/`, and `.opencode/plan/` are
   gitignored on purpose: private discovery research, scratch notes, and working plans. They are
   untracked by design — `git add -f` on any of them is a defect — and they exist on this machine
   only, so no clone carries them and `git clean -xdf` deletes them. Never treat them as the record:
   a conclusion worth keeping is promoted to an ADR, `CONTEXT.md`, or the spec (ADR-0008). Preserve
   what is still needed by copying it out of the repository — a separate private repository or a
   private archive — never by committing it here.

### Spec Kit

You MUST follow the memory-first workflow defined in
[`.specify/memory/workflow.md`](.specify/memory/workflow.md). Before planning, prepare context using the best
available path: MCP tools if configured, or `/speckit.memory-md.prepare-context` if Spec Kit commands are
available.

Project precedence: the constitution and [ADR-0008](docs/domain_knowledge/adr/0008-one-canonical-knowledge-layer.md)
come first. Durable memory (`docs/memory/`) and the wiki (`wiki/`) are **derived views** — no memory entry may
override the constitution, a feature artifact, `CONTEXT.md`, or an ADR. These memory steps are never
auto-executed; agreement 7 still governs.

## Environment

- Go: `go1.27.3 darwin/arm64` at `/Users/aiagent/.local/go/bin/go` (on `PATH`, `GOTOOLCHAIN=auto`).
  `go.mod` requires Go 1.26 minimum (`modernc.org/sqlite v1.62.0` declares `go 1.26.0`);
  CI targets 1.27.4.
- **If `go build` fails with `signal: killed` / exit 137**, the toolchain carries macOS quarantine:
  `xattr -dr com.apple.quarantine /Users/aiagent/.local/go`. Verify with `go tool compile -V`.
- Verify work with, in order:

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
CGO_ENABLED=0 go build ./...
```

- State directory for manual runs: `./.zamnius` (gitignored by convention, never commit it).
- `.pnpm-debug.log` is tool noise and is gitignored; do not commit it.

## Useful commands

```bash
/speckit.implement # implement the NEXT single unchecked task (all of T001–T068 are now done), verify it, then STOP and report for review
/speckit.analyze   # cross-artifact consistency check
```

## Conventions worth repeating

- `context.Context` first; wrap errors with `%w`; `slog` with key/value pairs; UUID identifiers;
  no `any` without justification; no transport imports in domain packages; no circular packages.
- Package layout: `cmd/`, `pkg/` (public contract), `internal/` (implementation), `test/`
  (integration, recoverability, audit, secrets, fakes, testdata).
- Uber Go Style Guide applies without exception (constitution §X).
