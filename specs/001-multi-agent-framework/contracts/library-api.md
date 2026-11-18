# Contract: embedded library surface

Package: `github.com/qi-lore/zamnius/pkg/zamnius`.
Related: constitution §VII (stable public contracts), [model-client.md](model-client.md), [store.md](store.md).

Anything importable by adopters lives here and is versioned under semantic versioning. Implementations
live under `internal/` and are injected at construction; no package under `internal/` may be imported
by an adopter.

---

## 1. Entry point

```go
package zamnius

// New builds an Engine. Every dependency is explicit; there are no package-level defaults
// and no init-time configuration.
func New(opts Options) (*Engine, error)

type Options struct {
    Store     StateStore      // required
    Artifacts ArtifactStore   // required
    Models    ModelRegistry   // required
    Logger    *slog.Logger    // optional; defaults to slog.DiscardHandler
    Tracer    trace.Tracer    // optional
    Clock     func() time.Time // optional; tests inject a deterministic clock
    IDSource  func() uuid.UUID // optional; tests inject deterministic ids
}
```

**Why explicit options**: the constitution forbids hidden global state and non-deterministic
behaviour; tests must be able to construct an engine with a frozen clock and scripted model.

## 2. Operations

```go
// Start validates def, creates a run, and drives it until terminal, paused, or ctx cancellation.
func (e *Engine) Start(ctx context.Context, def Definition, opts StartOptions) (RunResult, error)

// Resume continues an existing run. It never re-executes committed work.
func (e *Engine) Resume(ctx context.Context, runID uuid.UUID, opts ResumeOptions) (RunResult, error)

// Status reports the current position without acquiring the lease.
func (e *Engine) Status(ctx context.Context, runID uuid.UUID) (Status, error)

// Artifacts lists committed artifacts; content is fetched through ArtifactStore.
func (e *Engine) Artifacts(ctx context.Context, runID uuid.UUID) ([]ArtifactRef, error)

// Verify checks the event chain and every artifact fingerprint for a run.
func (e *Engine) Verify(ctx context.Context, runID uuid.UUID) (VerifyReport, error)
```

`StartOptions`: `AllowDefinitionDrift bool`, `MaxConcurrency int`, `Observer Observer`.

`ResumeOptions`: `Definition *Definition`, `AllowDefinitionDrift bool`, `MaxConcurrency int`, `Observer Observer`.
`Definition` is optional and defaults to nil, which resumes from the stored snapshot alone (FR-012).
When it is set, the engine compares its fingerprint with the run's recorded one and refuses a
mismatch with `ErrDefinitionDrift` unless `AllowDefinitionDrift` is set; the override is recorded as a
decision. The engine executes the stored snapshot either way, never the supplied workflow: committed
phases were recorded against the snapshot, so a different definition cannot be adopted mid-run
(data-model.md §10).

`RunResult`: `{ RunID uuid.UUID, Status RunStatus, Phases []PhaseResult, Artifacts []ArtifactRef, Err error }`.
A run that ends `failed` returns a non-nil `Err` **and** a populated `RunResult`, so callers never lose
the identifiers needed to inspect or resume.

## 3. Extension contracts

These are the stable seams the constitution names. This increment ships `ModelClient` fully and the
store contracts; `Tool`, `Memory`, and `Agent` extension points are declared only where the increment
uses them (agent identity), not as speculative interfaces.

```go
type ModelClient interface { /* see model-client.md */ }

type StateStore interface { /* see store.md */ }
type ArtifactStore interface { /* see store.md */ }

type ModelRegistry interface {
    // Client returns the client for a provider name, or an error naming known providers.
    Client(provider string) (ModelClient, error)
}

// Observer receives ordered engine facts. Implementations must be fast and must not block the
// engine: buffering or dropping is their responsibility.
type Observer interface {
    OnPhaseStarted(ctx context.Context, runID uuid.UUID, phase string)
    OnAttemptFinished(ctx context.Context, runID uuid.UUID, phase string, attempt int, class ErrorClass)
    OnPhaseFinished(ctx context.Context, runID uuid.UUID, phase string, status PhaseStatus)
}
```

## 4. Behavioural guarantees

1. **Idempotent resume** — `Resume` on a terminal run returns immediately with no state change.
2. **Ownership** — only one `Engine` instance may advance a run; a second returns `ErrRunAlreadyOwned`.
3. **No partial writes** — returned `RunResult` reflects only committed state.
4. **Context is honoured** — cancellation returns within one attempt boundary; the run is left `paused`
   and resumable.
5. **Determinism hooks** — with a frozen `Clock`, fixed `IDSource`, and scripted `ModelClient`, a run's
   event sequence is reproducible, which is what the fault-injection tests rely on.
6. **No secrets** — `Options`, `Definition`, and returned structs never carry raw credentials; the
   reference adapter reads its key from the environment.

## 5. Stability policy

- Additive changes (new fields with zero-value meaning, new interfaces) are minor versions.
- Removing or changing an exported signature is a major version.
- Every public contract change carries a compatibility note in the changelog (SC-016).
- `internal/` packages are not part of the contract and may change in any release.
