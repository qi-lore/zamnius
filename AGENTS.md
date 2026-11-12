# Zamnius — agent guide

Read this before touching the repository. It is the short entry point; the spec, plan, and
constitution are the authoritative sources.

## What this is

Zamnius is a recoverable multi-agent workflow framework in Go. Module path
`github.com/hIMEI29A/zamnius`. The committed increment covers **only** ordered phases with
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
decisions     docs/domain_knowledge/adr/0001–0006           architectural rationale
```

Chat history is never the source of truth. A fresh agent must be able to resume from these files
alone (constitution Principle II).

## Current state (2026-11-12)

- Spec is grilled and committed; 12 decisions recorded in `spec.md` → `## Clarifications`.
- Plan, research, data model, contracts, quickstart, and **`tasks.md`** are committed.
- `tasks.md` holds **68 TDD tasks in six phases**: Setup T001–T005, Foundational T006–T019
  (blocks every story), US1 T020–T042, US2 T043–T053, US3 T054–T062, Polish T063–T068.
- **The next step is `/speckit.implement`, starting at T001.** Run `/speckit.analyze` first if a
  cross-artifact consistency check is wanted before code exists.
- Branch: `001-multi-agent-framework`, 20 commits on top of `main` (which equals `dev`).
  Per the constitution, feature branches target `dev`, not `main`.
- No source code exists yet. No commit may claim behaviour that has no test.
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
/speckit.implement # next: execute tasks.md from T001
/speckit.analyze   # cross-artifact consistency check
```

## Conventions worth repeating

- `context.Context` first; wrap errors with `%w`; `slog` with key/value pairs; UUID identifiers;
  no `any` without justification; no transport imports in domain packages; no circular packages.
- Package layout: `cmd/`, `pkg/` (public contract), `internal/` (implementation), `test/`
  (integration, recoverability, audit, secrets, fakes, testdata).
- Uber Go Style Guide applies without exception (constitution §X).
