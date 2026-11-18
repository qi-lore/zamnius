package zamnius

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/audit"
	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/engine"
)

// Workflow and result vocabulary. Aliases, not copies: a value the engine
// returns is the same type an adopter names here.
type (
	// Definition is one workflow: ordered phases, optional fan-out with a join
	// rule, and the defaults every phase inherits.
	Definition = definition.Definition
	// RunResult is what a terminal or paused run returns. A failed run still
	// carries a populated result, so the caller never loses the run id it needs
	// to inspect or resume (contracts/library-api.md §2).
	RunResult = engine.RunResult
	// PhaseResult is the committed outcome of one phase inside a RunResult.
	PhaseResult = engine.PhaseResult
	// Status is the on-demand position report. Nothing in it is stored twice: it
	// is computed from the run row, its phases, and the tail of the event log.
	Status = engine.Status
	// StartOptions govern one Start call.
	StartOptions = engine.StartOptions
	// ResumeOptions govern one Resume call.
	ResumeOptions = engine.ResumeOptions
	// VerifyReport is the outcome of checking a run's chain and fingerprints. It
	// is returned even when verification fails, so the caller keeps the counts.
	VerifyReport = audit.Report
)

// Options are the engine's explicit dependencies. Every one is injected: there
// are no package-level defaults and no init-time configuration, so a test can
// build an engine with a frozen clock and a scripted model (constitution §VI).
type Options struct {
	// Store is the durable state port. Required.
	Store StateStore
	// Artifacts is the content-addressed artifact port. Required.
	Artifacts ArtifactStore
	// Models resolves a provider name from the definition to a client. Required,
	// because a definition names a provider but never constructs one (FR-059).
	Models ModelRegistry
	// Logger receives engine progress at Info and above. Optional; the default
	// discards output.
	Logger *slog.Logger
	// Clock reports the current instant. Optional; tests inject a frozen clock so
	// a run's timestamps and event sequence are reproducible.
	Clock func() time.Time
	// IDSource mints identifiers. Optional; tests inject a counter so run and
	// attempt ids are predictable.
	IDSource func() uuid.UUID
}

// Engine drives workflow runs. One instance may advance many runs, but only one
// engine at a time may own a given run: ownership is a conditional lease write,
// not a convention (ADR-0004).
type Engine struct {
	inner *engine.Engine
}

// New builds an Engine. Missing dependencies are refused rather than defaulted,
// because an engine without a store would silently lose work.
func New(opts Options) (*Engine, error) {
	// The model registry needs an adapter: the engine's port returns its own
	// client type, so a structurally identical but differently named interface
	// would otherwise fail Go's method-set identity rule.
	var models engine.Models
	if opts.Models != nil {
		models = modelsAdapter{registry: opts.Models}
	}

	inner, err := engine.New(engine.Options{
		Store:     opts.Store,
		Artifacts: opts.Artifacts,
		Models:    models,
		Logger:    opts.Logger,
		Clock:     opts.Clock,
		IDSource:  opts.IDSource,
	})
	if err != nil {
		return nil, err
	}
	return &Engine{inner: inner}, nil
}

// Start validates def, creates a run and its definition snapshot, takes
// ownership, and drives the run until it is terminal, paused, or ctx ends.
func (e *Engine) Start(ctx context.Context, def Definition, opts StartOptions) (RunResult, error) {
	return e.inner.Start(ctx, def, opts)
}

// Resume continues an existing run from the persisted store. It never
// re-executes committed work; a terminal run is a no-op (contracts/library-api.md §4).
func (e *Engine) Resume(ctx context.Context, runID uuid.UUID, opts ResumeOptions) (RunResult, error) {
	return e.inner.Resume(ctx, runID, opts)
}

// Status reports the current position without acquiring the lease, so a reader
// can never interfere with the owner.
func (e *Engine) Status(ctx context.Context, runID uuid.UUID) (Status, error) {
	return e.inner.Status(ctx, runID)
}

// Artifacts lists every committed artifact of a run. Content is fetched through
// the artifact store, never returned inline.
func (e *Engine) Artifacts(ctx context.Context, runID uuid.UUID) ([]ArtifactRef, error) {
	return e.inner.Artifacts(ctx, runID)
}

// Verify checks the event chain and every artifact fingerprint of a run. A
// problem is reported, not repaired: silently rebuilding a tampered record would
// defeat the point of having one (T059).
func (e *Engine) Verify(ctx context.Context, runID uuid.UUID) (VerifyReport, error) {
	return e.inner.Verify(ctx, runID)
}

// modelsAdapter bridges the adopter-facing registry to the engine's port. The
// two interfaces differ only in the name of the client type they return, which
// is enough for Go to treat them as unrelated.
type modelsAdapter struct {
	registry ModelRegistry
}

// Client resolves a provider name, refusing a registry that answers with no
// client so the failure lands at the provider boundary rather than as a nil
// dereference inside a phase.
func (a modelsAdapter) Client(provider string) (engine.Model, error) {
	client, err := a.registry.Client(provider)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   fmt.Sprintf("no model client for provider %q", provider),
			Hint:  "register the provider with a model registry before starting the run",
		}
	}
	return client, nil
}
