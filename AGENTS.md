# Zamnius — agent guide

Read this before touching the repository. It is the short entry point; the spec, plan, and
constitution are the authoritative sources.

## What this is

Zamnius is a recoverable multi-agent workflow framework in Go. Module path
`github.com/prompt-fu/zamnius`. The committed increment covers **only** ordered phases with
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

## Current state (2026-11-15)

- Spec is grilled and committed; 12 decisions recorded in `spec.md` → `## Clarifications`.
- Plan, research, data model, contracts, quickstart, and **`tasks.md`** are committed.
- `tasks.md` holds **68 TDD tasks in six phases**: Setup T001–T005, Foundational T006–T019
  (blocks every story), US1 T020–T042, US2 T043–T053, US3 T054–T062, Polish T063–T068.
- **Implementation is well under way: T001–T051 are done and tested, so US1 is complete, US2's
  genuine-process-death, ownership/idempotence, attempt-reconciliation, lease-heartbeat,
  definition-drift, schema-version, and `run resume` proofs are in place, and T043–T051 are all
  committed. The next action is to resume US2 at T052** (SIGINT/SIGTERM handling in
  `cmd/zamnius/main.go`: cancel the context so the engine commits the attempt boundary, releases the
  lease, and leaves the run `paused` with exit `7`; a second signal exits immediately with `1`). For
  the exact per-task state — done, implemented-but-untested, or untouched — and the design decisions
  taken along the way, read `tasks.md` → `## Implementation status (2026-11-12)`; that section is
  authoritative until the boxes above it are updated.
- Branch: `001-multi-agent-framework`, commits above the initial commit `0a512c3`. Per the
  constitution, feature branches target `dev`, not `main`. Working tree is clean; every commit is
  local.
- **Ignore files are shared, not branch-local.** `dev` and `main` both derive from the initial
  commit `0a512c3`. On **2026-11-13** `dev` was advanced by the two ignore commits the feature
  branch already carried (`fbde31c`, `0878256`), so both branches now hold **byte-identical**
  `.gitignore` and `.specify/.gitignore`. Because the change is identical on both sides, a
  `feature → dev` merge is conflict-free on those files — verified with
  `git merge-tree --write-tree dev 001-multi-agent-framework`, which returns a clean tree. The
  branches have therefore diverged (the feature branch is no longer a descendant of `dev`), so
  integration is a real 3-way merge. Add an ignore rule in one place and mirror the exact content;
  a divergent edit on a single branch is what re-introduces the conflict.
- Source now exists: `cmd/zamnius`, `internal/{app,audit,cli,config,definition,domain,engine,fault,model,observability,store}`,
  `pkg/zamnius`, and tests under `test/{fakes,integration,recoverability,testdata}`. No commit
  may claim behaviour that has no test.
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
/speckit.implement # implement the NEXT single unchecked task (now T052), verify it, then STOP and report for review
/speckit.analyze   # cross-artifact consistency check
```

## Conventions worth repeating

- `context.Context` first; wrap errors with `%w`; `slog` with key/value pairs; UUID identifiers;
  no `any` without justification; no transport imports in domain packages; no circular packages.
- Package layout: `cmd/`, `pkg/` (public contract), `internal/` (implementation), `test/`
  (integration, recoverability, audit, secrets, fakes, testdata).
- Uber Go Style Guide applies without exception (constitution §X).
