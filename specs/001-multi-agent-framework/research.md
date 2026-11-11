# Phase 0 Research: Zamnius Core Increment (001)

**Feature**: `specs/001-multi-agent-framework/spec.md`
**Date**: 2026-11-11
**Module path**: `github.com/hIMEI29A/zamnius`
**Scope**: stories P1–P3 only — ordered phases with fan-out/join, resumability, audit reconstruction.

All decisions below were verified against live module metadata on 2026-11-11.

---

## R1. Go toolchain version

**Decision**: Require Go 1.25 or newer in `go.mod` (`go 1.25.0`), develop and CI against the current stable release, which is **Go 1.27.4** (verified at `go.dev/VERSION?m=text`, released 2026-11-02).

**Rationale**: The spec and constitution require Go 1.25+. Pinning the *minimum* at 1.25 keeps the module usable by adopters who have not upgraded, while CI on 1.27.4 catches modern-toolchain breakage. No approved feature depends on a post-1.25 API, so a higher floor would discard users for nothing.

**Alternatives considered**: Pinning `go 1.27` (simple, but needlessly narrow); omitting the version (invalid for modules since 1.21).

**Consequence for this environment**: `go` **is** available. Verified 2026-11-11: `go1.27.3 darwin/arm64` at `/Users/aiagent/.local/go/bin/go` (on `PATH`, `GOTOOLCHAIN=auto`). Development runs on 1.27.3; CI targets the latest stable, 1.27.4. No toolchain installation task is required.

**Toolchain caveat (encountered and resolved)**: the installed tree carried `com.apple.quarantine`, so every tool binary was killed on exec — `go build` and `go tool compile -V` both failed with exit 137 and the misleading message `signal: killed`. Clearing the attribute (`xattr -dr com.apple.quarantine /Users/aiagent/.local/go`) fixed it; afterwards `go build`, `go vet`, `go test -count=1`, `go test -race`, and `CGO_ENABLED=0 go build` all pass. Any future fresh install on this host should be checked the same way before diagnosing a phantom compilation failure.

---

## R2. Durable state store

**Decision**: SQLite via **`modernc.org/sqlite` v1.62.0** (pure Go, no cgo), opened in WAL mode with `busy_timeout`, `foreign_keys=ON`, and a single writer connection.

**Rationale**: Pure Go removes cgo from the build, which keeps `GOOS/GOARCH` cross-compilation and offline CI working — a direct requirement of the constitution's "tests must run offline where possible". `mattn/go-sqlite3` requires cgo and a C toolchain, making the reference build hostile to contributors. WAL plus one writer connection gives the transaction semantics the recoverability design depends on (state change + event commit atomically) without a second process.

**Alternatives considered**:
- `mattn/go-sqlite3` (cgo): rejected for toolchain friction and cross-compile cost.
- BoltDB/bbolt: transactional and pure Go, but no SQL for audit queries and a less familiar operational story.
- Files + journal: rejected in the spec (ADR-0005) as the riskiest code in the increment.

**Verified**: v1.62.0 published 2026-11-07.

---

## R3. Artifact storage

**Decision**: Content-addressed files under `<state-dir>/artifacts/<sha256[0:2]>/<sha256>`, written `temp → fsync → rename`, with the database row inserted only after the rename succeeds.

**Rationale**: Fingerprints make artifacts verifiable against the audit record (FR-023, story 3 scenario 4), and rename-on-same-filesystem is atomic on POSIX, satisfying FR-015 without a distributed transaction. Writing the file before the row means the only reachable inconsistency is an orphaned file, which a sweep can delete; the reverse order could produce a row pointing at nothing, which breaks resume.

**Alternatives considered**: Store blobs in SQLite (simple backup, but artifacts stop being inspectable — rejected in ADR-0005); write row first (unsafe crash window).

---

## R4. Configuration decoding (YAML / JSON / TOML)

**Decision**: One decoder per format into a single strict struct set:
- YAML: **`github.com/goccy/go-yaml` v1.19.2** with strict mode (unknown fields rejected).
- TOML: **`github.com/pelletier/go-toml/v2` v2.4.3** with `DisallowUnknownFields`.
- JSON: stdlib `encoding/json` with `DisallowUnknownFields`.

**Rationale**: The constitution requires all three formats (ADR in spec Q2). `goccy/go-yaml` is actively maintained, supports YAML 1.2 and strict decoding, and avoids the frozen `gopkg.in/yaml.v3` (last release 2022) whose strict mode is coarser. Strictness is what makes FR-052 (refuse invalid config naming the offending setting) achievable uniformly.

**Alternatives considered**: `gopkg.in/yaml.v3` v3.0.1 (stable but frozen; weaker strict/error reporting); `BurntSushi/toml` (module path no longer resolves cleanly at the proxy — 404 during verification, so it was dropped immediately).

**Precedence** (FR-051): CLI flags > environment variables (`ZAMNIUS_*`) > explicit `--config` file > discovered config (`zamnius.yaml`, `.zamnius.yaml`, `zamnius.json`, `zamnius.toml`) > built-in defaults. Conflicting duplicates inside one file are an error, not silently last-wins.

---

## R5. CLI surface

**Decision**: Standard library `flag` with a small subcommand dispatcher; **no CLI framework**.

**Rationale**: The increment needs four operations (start, resume, status, artifacts). Cobra v1.10.2 buys nested help, completion, and shell integration we do not need, and the constitution's YAGNI principle (IX) plus "external dependencies must provide clear value" points the other way. Stdlib `flag` also keeps the dependency graph auditable for a framework whose main risk is scope creep.

**Alternatives considered**: `spf13/cobra` v1.10.2 (real value once HTTP/gRPC and plugin commands exist — revisit in a later increment); `urfave/cli` (same objection).

---

## R6. Concurrency for fan-out

**Decision**: `golang.org/x/sync/errgroup` v0.25.0 with a weighted semaphore for the concurrency cap (default 8, configurable).

**Rationale**: Fan-out branches need "cancel siblings on first fatal error" and "wait for all" semantics in one primitive; `errgroup.WithContext` plus `SetLimit` provides both with no hand-rolled WaitGroup bookkeeping. Goroutine leak safety is covered by context cancellation on run cancel (FR-008).

**Alternatives considered**: raw `sync.WaitGroup` + `context` (more code, easy to leak on early error); worker-pool channel (needed only when a queue outlives a phase).

---

## R7. Identifiers and time

**Decision**: `github.com/google/uuid` v1.6.0 for run/attempt/artifact/event-correlation identifiers (UUIDv4, stored as text). Ordering comes from the database `seq` column, never from wall-clock time.

**Rationale**: FR-021 and the clock-skew edge case require sequence-based ordering. UUIDs are required by the constitution (strong ID types) and avoid exposing sequence counts in CLI output.

**Alternatives considered**: ULID (sortable, but adds a dependency for ordering we already get from SQLite); integer IDs (fine internally, but they leak volume and complicate export/import).

---

## R8. Observability

**Decision**: `log/slog` for structured logs (stdlib), OpenTelemetry **v1.47.0** traces and metrics behind a thin internal interface with a no-op default.

**Rationale**: The constitution mandates structured logs, traces, and metrics. OTel is the ecosystem standard and its Go packages are current (v1.47.0, 2026-10-02). Keeping OTel behind an interface means the core packages stay testable without an exporter and library adopters are not forced into a global provider.

**Alternatives considered**: Prometheus client only (no traces); no tracing in this increment (rejected: audit and debugging of resume are the point of the increment).

---

## R9. Model access

**Decision**: Provider-neutral `ModelClient` contract (request: system prompt, messages, max output tokens, optional response schema; response: content, token usage, stop reason; error classification). Reference adapter: OpenAI-compatible Chat Completions over HTTP with configurable base URL, using `response_format` JSON schema where the server supports it and validating locally otherwise.

**Rationale**: One adapter covers OpenAI, Azure-compatible gateways, vLLM, Ollama, and OpenRouter, maximising reach without coupling the core (ADR-0006). Local schema validation is mandatory because compatibility servers vary in structured-output support.

**Alternatives considered**: Anthropic Messages (deeper structured-output ergonomics, single vendor); Gemini (strong free tier, vendor-specific).

**Not in this increment**: streaming, tool calling, embeddings.

---

## R10. Deterministic tests and fault injection

**Decision**: stdlib `testing` + `google/go-cmp` v0.7.0; a scripted fake `ModelClient` compiled into the test binary; a named `faultpoint` hook that panics at labelled boundaries (before/after artifact rename, before/after transaction commit, before lease renewal).

**Rationale**: SC-001/SC-002/SC-003 need interruptions at exact commit boundaries, which is impossible to reach reliably with real providers. In-process fault points cover boundary correctness; one subprocess `SIGKILL` test per resume path covers genuine process death (re-exec pattern in `TestMain`). `go-cmp` gives readable diffs without testify's assertion DSL.

**Alternatives considered**: `testify` (popular, but its `require`/`assert` idiom conflicts with the constitution's explicit-error style and adds little over `cmp.Diff`); `go.uber.org/goleak` (consider later; not needed for the first increment).

---

## R11. Lease and takeover mechanics

**Decision**: Lease stored on the `runs` row (`owner_id`, `lease_expires_at`). Acquisition is a conditional `UPDATE ... WHERE lease_expires_at IS NULL OR lease_expires_at < :now` inside a transaction; heartbeat renews at lease/3; a takeover writes a `run.taken_over` event carrying the previous owner and the expiry evidence. Default lease 30s, heartbeats 10s.

**Rationale**: This satisfies FR-016 and US2 scenario 5 without a distributed lock (ADR-0004), and the conditional update is atomic in SQLite. Values are defaults, overridable by configuration.

**Alternatives considered**: OS file locks (fail on network filesystems, invisible to audit); advisory locks in a second service (new operational dependency).

---

## R12. Event log integrity

**Decision**: Append-only `events` table with `seq INTEGER PRIMARY KEY AUTOINCREMENT`, canonical-JSON payload, and `sha256(prev_hash || canonical_payload)` chained into `hash`. Verification is a streaming recomputation command in the audit package; artifacts are verified by fingerprint.

**Rationale**: Story 3 requires immutable, orderable records and rejection of later modification (scenario 3). A hash chain makes tampering evident without a separate notarisation service, and it costs one column.

**Alternatives considered**: plain append-only table with triggers (weaker evidence); external ledger (overkill).

---

## R13. Budget accounting

**Decision**: Attempt count, wall-clock time, and tokens (FR-005, FR-007) enforced by the executor, which checks remaining allowance before each attempt and after each model response; exhaustion writes a `phase.budget_exhausted` event naming `attempts`, `wall_clock`, or `tokens` and applies the declared failure policy.

**Rationale**: Checking before *and* after means a single over-large response cannot silently exceed the cap without a record. Tokens are taken from the provider response's usage block; when a server omits usage, the response is rejected rather than guessed (recorded as `invalid_response`).

**Alternatives considered**: enforcing only between phases (lets one phase overspend unboundedly); cost budgets (excluded by spec decision).

---

## R14. Packaging and distribution

**Decision**: Single Go module producing one binary (`cmd/zamnius`) plus an importable library (`pkg/zamnius`). Container image and release automation are **out of scope** for this increment and recorded as a follow-on chore.

**Rationale**: Adopters evaluating a framework need `go install` and `go get` to work first; image publishing without a running system would be premature.

**Alternatives considered**: multi-module workspace (no benefit until a plugin SDK exists); Homebrew/Docker from day one (distribution without a stable surface).
