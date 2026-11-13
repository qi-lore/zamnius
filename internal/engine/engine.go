// Package engine executes a workflow definition against a durable store. Its
// central rule is that every state change is committed in the same transaction as
// the events and decisions that describe it (FR-011), so a reader can always
// reconstruct why the run is where it is, and a crash can always be resumed
// without repeating committed work (ADR-0003).
package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/prompt-fu/zamnius/internal/audit"
	"github.com/prompt-fu/zamnius/internal/definition"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// Default execution settings. They are constants because a silently different
// default would change the observable behaviour of a resumed run.
const (
	// DefaultMaxConcurrency caps parallel branches (contracts/store.md §2, FR-004).
	DefaultMaxConcurrency = 8
	// DefaultLeaseTTL is the ownership window; renewal happens at TTL/3 (T048).
	DefaultLeaseTTL = 30 * time.Second
)

// Model is the model port as the engine sees it. It is declared here rather than
// imported so the engine depends on behaviour, not on the package that ships the
// reference adapter (Go's structural typing makes any implementation fit).
type Model interface {
	Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error)
}

// Models resolves a provider name to a client.
type Models interface {
	Client(provider string) (Model, error)
}

// Store is the durable state port (contracts/store.md §2).
type Store interface {
	CreateRun(ctx context.Context, spec domain.RunSpec) (domain.Run, error)
	LoadRun(ctx context.Context, id uuid.UUID) (domain.Run, error)

	AcquireLease(ctx context.Context, id uuid.UUID, owner string, ttl time.Duration) (domain.Lease, error)
	RenewLease(ctx context.Context, l domain.Lease, ttl time.Duration) (domain.Lease, error)
	ReleaseLease(ctx context.Context, l domain.Lease) error

	CommitAttemptResult(ctx context.Context, r domain.AttemptCommit) error
	CommitPhaseResult(ctx context.Context, r domain.PhaseCommit) error
	CommitRunResult(ctx context.Context, r domain.RunCommit) error

	Phases(ctx context.Context, runID uuid.UUID) ([]domain.PhaseState, error)
	Attempts(ctx context.Context, runID uuid.UUID, phase string) ([]domain.Attempt, error)
	Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]domain.Event, error)
	Decisions(ctx context.Context, runID uuid.UUID) ([]domain.Decision, error)
}

// Artifacts is the content-addressed artifact port (contracts/store.md §4).
type Artifacts interface {
	Put(ctx context.Context, runID uuid.UUID, kind, mediaType string, r io.Reader) (domain.ArtifactRef, error)
	Get(ctx context.Context, sha string) (io.ReadCloser, error)
	Verify(ctx context.Context, sha string) error
}

// Observer receives ordered engine facts. Implementations must be fast and must
// not block the engine (contracts/library-api.md §3).
type Observer interface {
	OnPhaseStarted(ctx context.Context, runID uuid.UUID, phase string)
	OnAttemptFinished(ctx context.Context, runID uuid.UUID, phase string, attempt int, class domain.ErrorClass)
	OnPhaseFinished(ctx context.Context, runID uuid.UUID, phase string, status domain.PhaseStatus)
}

// Options are the engine's explicit dependencies. There are no package-level
// defaults and no init-time configuration (constitution §VI).
type Options struct {
	Store     Store
	Artifacts Artifacts
	Models    Models
	Logger    *slog.Logger
	Clock     func() time.Time
	IDSource  func() uuid.UUID
	// Sleep waits between retries. Tests inject an immediate return so a backoff
	// policy can be asserted without spending wall-clock time.
	Sleep func(ctx context.Context, d time.Duration) error
	// Owner identifies this process in the lease. A distinct value per process is
	// what makes takeover evidence meaningful (ADR-0004).
	Owner string
	// LeaseTTL overrides DefaultLeaseTTL.
	LeaseTTL time.Duration
}

// Engine drives runs. One instance may advance many runs, but only one engine
// instance owns a given run at a time.
type Engine struct {
	store     Store
	artifacts Artifacts
	models    Models
	logger    *slog.Logger
	now       func() time.Time
	ids       func() uuid.UUID
	sleep     func(ctx context.Context, d time.Duration) error
	owner     string
	leaseTTL  time.Duration
}

// StartOptions govern one Start call.
type StartOptions struct {
	AllowDefinitionDrift bool
	MaxConcurrency       int
	Observer             Observer
}

// ResumeOptions govern one Resume call.
type ResumeOptions struct {
	// Definition is the workflow the caller believes the run is executing, usually a
	// fresh parse of the file it was started from. When non-nil it is compared with
	// the run's recorded fingerprint, so an operator editing the file learns that the
	// run has drifted; when nil the run resumes from its stored snapshot alone
	// (FR-012). Either way the engine executes the snapshot, never a replacement.
	Definition           *definition.Definition
	AllowDefinitionDrift bool
	MaxConcurrency       int
	Observer             Observer
}

// PhaseResult is the committed outcome of one phase.
type PhaseResult struct {
	Name       string               `json:"name"`
	Status     domain.PhaseStatus   `json:"status"`
	Attempts   int                  `json:"attempts"`
	Artifacts  []domain.ArtifactRef `json:"artifacts,omitempty"`
	ErrorClass domain.ErrorClass    `json:"error_class,omitempty"`
}

// RunResult is what every terminal or paused run returns. A failed run still
// carries a populated result, so the caller never loses the identifiers it needs
// to inspect or resume (contracts/library-api.md §2).
type RunResult struct {
	RunID     uuid.UUID            `json:"run_id"`
	Status    domain.RunStatus     `json:"status"`
	Phases    []PhaseResult        `json:"phases"`
	Artifacts []domain.ArtifactRef `json:"artifacts,omitempty"`
	Err       error                `json:"-"`
}

// Status is the on-demand position report; nothing here is stored twice.
type Status struct {
	RunID        uuid.UUID           `json:"run_id"`
	Status       domain.RunStatus    `json:"status"`
	CurrentPhase *string             `json:"current_phase,omitempty"`
	Phases       []domain.PhaseState `json:"phases"`
	Budget       domain.Budget       `json:"budget"`
	Spent        domain.Consumption  `json:"spent"`
	// OwnerID and LeaseExpiresAt are what explain a refused resume: they name the
	// process holding the run and when its claim lapses (contracts/cli.md §1).
	OwnerID        *string        `json:"owner_id,omitempty"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
	NextAction     string         `json:"next_action"`
	LastEvents     []domain.Event `json:"last_events,omitempty"`
}

// New builds an engine. Missing dependencies are refused rather than defaulted:
// an engine with no store would silently lose work.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "engine requires a state store",
			Hint:  "pass Options.Store; without it a run cannot be made durable",
		}
	}
	if opts.Artifacts == nil {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "engine requires an artifact store",
			Hint:  "pass Options.Artifacts; results are content-addressed, not inline",
		}
	}
	if opts.Models == nil {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "engine requires a model registry",
			Hint:  "pass Options.Models; a definition names a provider but never constructs one",
		}
	}

	engine := &Engine{
		store:     opts.Store,
		artifacts: opts.Artifacts,
		models:    opts.Models,
		logger:    opts.Logger,
		now:       opts.Clock,
		ids:       opts.IDSource,
		sleep:     opts.Sleep,
		owner:     opts.Owner,
		leaseTTL:  opts.LeaseTTL,
	}
	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}
	if engine.now == nil {
		engine.now = func() time.Time { return time.Now().UTC() }
	}
	if engine.ids == nil {
		engine.ids = uuid.New
	}
	if engine.sleep == nil {
		engine.sleep = sleepContext
	}
	if engine.owner == "" {
		engine.owner = defaultOwner()
	}
	if engine.leaseTTL <= 0 {
		engine.leaseTTL = DefaultLeaseTTL
	}
	return engine, nil
}

// Start validates the definition, creates the run and its snapshot, takes
// ownership, and drives it to a terminal or paused state.
func (e *Engine) Start(ctx context.Context, def definition.Definition, opts StartOptions) (RunResult, error) {
	normalized, err := definition.Normalize(def)
	if err != nil {
		return RunResult{}, err
	}
	snapshot, err := definition.Snapshot(normalized)
	if err != nil {
		return RunResult{}, err
	}
	fingerprint, err := definition.Fingerprint(normalized)
	if err != nil {
		return RunResult{}, err
	}

	runID := e.ids()
	states := make([]domain.PhaseState, 0, len(normalized.Phases))
	for i, phase := range normalized.Phases {
		states = append(states, domain.PhaseState{
			RunID:   runID,
			Name:    phase.Name,
			Ordinal: i,
			Status:  domain.PhasePending,
		})
	}

	budget := domain.Budget{}
	if normalized.Budget != nil {
		budget = *normalized.Budget
	}

	now := e.now()
	run, err := e.store.CreateRun(ctx, domain.RunSpec{
		ID:                    runID,
		DefinitionName:        normalized.Name,
		DefinitionFingerprint: fingerprint,
		DefinitionJSON:        snapshot,
		Budget:                budget,
		Phases:                states,
		Now:                   now,
	})
	if err != nil {
		return RunResult{RunID: runID, Err: err}, err
	}

	lease, err := e.store.AcquireLease(ctx, run.ID, e.owner, e.leaseTTL)
	if err != nil {
		return RunResult{RunID: run.ID, Status: run.Status, Err: err}, err
	}

	observer := opts.Observer
	session := &session{
		engine:         e,
		run:            run,
		def:            normalized,
		lease:          lease,
		observer:       observer,
		maxConcurrency: clampConcurrency(opts.MaxConcurrency),
		kinds:          make(map[string]map[string]struct{}),
		artifacts:      make(map[string][]domain.ArtifactRef),
		recovered:      make(map[string]map[string]recoveredAttempt),
	}
	// The lease acquisition advanced record_version, so the in-memory run is
	// refreshed before any guarded commit; a stale version would defeat the guard.
	if err := session.refresh(ctx); err != nil {
		return session.result(err), err
	}
	session.loadPhaseKinds()

	e.logger.Info("run started", "run_id", run.ID.String(), "definition", normalized.Name)

	if err := session.commitRun(ctx, domain.RunDelta{Status: domain.RunRunning}, []domain.EventDraft{{
		Type:  domain.EventRunStarted,
		Actor: domain.ActorSystem,
		Payload: map[string]any{
			"definition_fingerprint": fingerprint,
			"phases":                 len(normalized.Phases),
		},
	}}, nil, true); err != nil {
		return session.result(err), err
	}

	return session.drive(ctx)
}

// Status reports the current position without acquiring the lease, so a reader
// can never interfere with the owner.
func (e *Engine) Status(ctx context.Context, runID uuid.UUID) (Status, error) {
	run, err := e.store.LoadRun(ctx, runID)
	if err != nil {
		return Status{}, err
	}
	phases, err := e.store.Phases(ctx, runID)
	if err != nil {
		return Status{}, err
	}
	events, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return Status{}, err
	}

	status := Status{
		RunID:          run.ID,
		Status:         run.Status,
		CurrentPhase:   run.CurrentPhase,
		Phases:         phases,
		Budget:         run.Budget,
		Spent:          run.Spent,
		OwnerID:        run.OwnerID,
		LeaseExpiresAt: run.LeaseExpiresAt,
		NextAction:     nextAction(run, phases),
	}
	if len(events) > 5 {
		events = events[len(events)-5:]
	}
	status.LastEvents = events
	return status, nil
}

// Artifacts lists the run's committed artifacts in commit order. It walks
// phases→attempts because that is the record of provenance; the artifact table is
// an index, not the truth.
func (e *Engine) Artifacts(ctx context.Context, runID uuid.UUID) ([]domain.ArtifactRef, error) {
	// A store that indexes artifacts can answer directly; the walk below is the
	// fallback, and it also sees run-level artifacts (an aggregate belongs to its
	// phase, not to an attempt) that a per-attempt walk cannot reach.
	if index, ok := e.store.(interface {
		Artifacts(ctx context.Context, runID uuid.UUID) ([]domain.ArtifactRef, error)
	}); ok {
		refs, err := index.Artifacts(ctx, runID)
		if err == nil {
			return refs, nil
		}
	}

	phases, err := e.store.Phases(ctx, runID)
	if err != nil {
		return nil, err
	}
	var refs []domain.ArtifactRef
	for _, phase := range phases {
		attempts, err := e.store.Attempts(ctx, runID, phase.Name)
		if err != nil {
			return nil, err
		}
		for _, attempt := range attempts {
			refs = append(refs, attempt.Artifacts...)
		}
	}
	return refs, nil
}

// Verify checks the event chain and every artifact fingerprint. It returns the
// report even when verification fails, because the counts are part of the answer.
func (e *Engine) Verify(ctx context.Context, runID uuid.UUID) (audit.Report, error) {
	run, err := e.store.LoadRun(ctx, runID)
	if err != nil {
		return audit.Report{}, err
	}
	events, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return audit.Report{}, err
	}
	refs, err := e.Artifacts(ctx, runID)
	if err != nil {
		return audit.Report{}, err
	}

	problems := audit.VerifyChain(events)
	problems = append(problems, audit.VerifyArtifacts(ctx, refs, e.artifacts)...)

	report := audit.Report{
		RunID:      run.ID,
		Events:     len(events),
		Artifacts:  len(refs),
		VerifiedAt: e.now(),
		Problems:   problems,
	}
	if !report.OK() {
		e.logger.Error("verification failed", "run_id", run.ID.String(), "problems", len(problems))
	}
	return report, nil
}

// session is the mutable execution state of one run inside one engine call.
type session struct {
	engine         *Engine
	run            domain.Run
	def            definition.Definition
	observer       Observer
	maxConcurrency int

	// lease is the current ownership claim. The heartbeat goroutine renews it while
	// the drive loop runs, so every access goes through leaseMu (T048, ADR-0004).
	leaseMu sync.Mutex
	lease   domain.Lease
	// leaseLost records why ownership was lost when a renewal failed. The drive loop
	// reads it to pause with the real cause instead of a generic cancellation.
	leaseLost error
	// stopHeartbeat stops the renewal goroutine and waits for it to exit. It is set
	// by drive and is idempotent, so releaseLease and the deferred stop can both call
	// it without racing a renewal against the release.
	stopHeartbeat func()

	// attemptNo allocates 1-based attempt numbers per phase. Fan-out branches
	// allocate concurrently, and attempt_no is unique per (run, phase), so the
	// counter must be guarded.
	mu        sync.Mutex
	attemptNo map[string]int
	// recovered holds, per phase and agent, the durable result of an attempt that
	// had already succeeded before the interruption. A resumed run reuses it rather
	// than recomputing, so a completed step is never re-executed (FR-013, SC-002).
	recovered map[string]map[string]recoveredAttempt
	// restartFrom holds, per phase and agent, the attempt that reconciliation found
	// without a committed outcome. The next attempt of that agent claims it as
	// restarted_from, so a restart names the attempt it replaced (data-model.md §4
	// invariant 2).
	restartFrom map[string]map[string]uuid.UUID
	// kinds records the artifact kinds produced per phase, which the outputs
	// invariant checks before a phase may succeed.
	kinds     map[string]map[string]struct{}
	artifacts map[string][]domain.ArtifactRef
	results   map[string]PhaseResult
	// spent accumulates this session's consumption; the store receives it as
	// deltas so two branches cannot erase each other's usage. committed is what the
	// store has already recorded, so the delta of a phase is the difference and a
	// second commit cannot count the same tokens twice.
	spent     domain.Consumption
	committed domain.Consumption
}

// drive walks the declared phase order. Order, not concurrency, is the contract
// for phases; branches are the only parallel dimension (FR-002).
func (s *session) drive(ctx context.Context) (RunResult, error) {
	// The heartbeat renews the lease while phases run, so a phase longer than the
	// TTL cannot let ownership lapse mid-work (T048, ADR-0004). It cancels ctx on a
	// failed renewal, which stops the loop below and pauses with the recorded cause.
	ctx, stop := s.startHeartbeat(ctx)
	defer stop()

	phases, err := s.engine.store.Phases(ctx, s.run.ID)
	if err != nil {
		return s.result(err), err
	}
	stateOf := make(map[string]domain.PhaseState, len(phases))
	for _, phase := range phases {
		stateOf[phase.Name] = phase
	}

	for i, phase := range s.def.Phases {
		if ctx.Err() != nil {
			return s.pause(ctx, "context ended before phase "+phase.Name)
		}
		state := stateOf[phase.Name]

		// A resumed run skips phases that already reached a terminal state; this
		// is what makes resume idempotent rather than a re-execution (FR-013).
		if state.Status.Terminal() {
			s.recordResult(PhaseResult{Name: phase.Name, Status: state.Status, Attempts: state.AttemptCount})
			continue
		}

		if phase.When != nil && !s.conditionHolds(phase, stateOf) {
			if err := s.skipPhase(ctx, i, phase); err != nil {
				return s.result(err), err
			}
			continue
		}

		if s.observer != nil {
			s.observer.OnPhaseStarted(ctx, s.run.ID, phase.Name)
		}

		outcome, runErr := s.runPhase(ctx, i, phase)
		if runErr != nil {
			return s.result(runErr), runErr
		}

		if outcome.Status == domain.PhaseCancelled {
			return s.pause(ctx, "cancelled during phase "+phase.Name)
		}

		if outcome.Status == domain.PhaseFailed || outcome.Status == domain.PhaseDegraded {
			stopped, err := s.applyFailurePolicy(ctx, i, phase, outcome)
			if err != nil {
				return s.result(err), err
			}
			if stopped != nil {
				return *stopped, stopped.Err
			}
		}

		refreshed, err := s.engine.store.Phases(ctx, s.run.ID)
		if err != nil {
			return s.result(err), err
		}
		for _, updated := range refreshed {
			stateOf[updated.Name] = updated
		}
	}

	// Ownership lost while the last phase ran: the run must pause, not claim success
	// it may no longer be entitled to write (ADR-0004).
	if cause := s.leaseLossCause(); cause != nil {
		return s.pauseWithCause(ctx, "lease renewal failed: ownership of the run was lost", cause)
	}

	reason := "all phases reached a terminal state"
	if err := s.commitRun(ctx, domain.RunDelta{Status: domain.RunSucceeded, OutcomeReason: &reason},
		[]domain.EventDraft{{
			Type:    domain.EventRunSucceeded,
			Actor:   domain.ActorSystem,
			Payload: map[string]any{"phases": len(s.def.Phases), "reason": reason},
		}}, nil, false); err != nil {
		return s.result(err), err
	}

	s.releaseLease(context.WithoutCancel(ctx))
	result := s.result(nil)
	result.Status = domain.RunSucceeded
	s.engine.logger.Info("run finished", "run_id", s.run.ID.String(), "status", string(result.Status))
	return result, nil
}

// conditionHolds evaluates a when-condition against already-recorded outcomes.
func (s *session) conditionHolds(phase definition.Phase, states map[string]domain.PhaseState) bool {
	condition := phase.When
	prior, ok := states[condition.AfterPhase]
	if !ok {
		return false
	}
	switch condition.Outcome {
	case definition.OutcomeSucceeded:
		return prior.Status == domain.PhaseSucceeded
	case definition.OutcomeFailed:
		return prior.Status == domain.PhaseFailed || prior.Status == domain.PhaseDegraded
	case definition.OutcomeAny:
		return prior.Status.Terminal()
	default:
		return false
	}
}

// recordResult stores the phase outcome for the returned RunResult.
func (s *session) recordResult(result PhaseResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.results == nil {
		s.results = make(map[string]PhaseResult)
	}
	s.results[result.Name] = result
}

// result assembles the RunResult from committed state only.
func (s *session) result(err error) RunResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := RunResult{RunID: s.run.ID, Status: s.run.Status, Err: err}
	artifacts := make([]domain.ArtifactRef, 0)
	for _, phase := range s.def.Phases {
		if recorded, ok := s.results[phase.Name]; ok {
			result.Phases = append(result.Phases, recorded)
			artifacts = append(artifacts, recorded.Artifacts...)
			continue
		}
		result.Phases = append(result.Phases, PhaseResult{Name: phase.Name, Status: domain.PhasePending})
	}
	result.Artifacts = artifacts
	return result
}

// clampConcurrency applies the default and a sane upper bound.
func clampConcurrency(requested int) int {
	if requested <= 0 {
		return DefaultMaxConcurrency
	}
	return requested
}

// defaultOwner identifies this process without exposing anything sensitive.
func defaultOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

// sleepContext is the production backoff wait. It returns the context error so a
// cancelled run stops between attempts rather than sleeping through the signal.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// errorClassOf extracts the class from an arbitrary error, defaulting to internal
// as the contract requires (contracts/model-client.md §4).
func errorClassOf(err error) domain.ErrorClass {
	if err == nil {
		return ""
	}
	return domain.ClassOf(err)
}

// isContextStop reports whether an error is the result of the caller ending the
// run, which must pause rather than fail it.
func isContextStop(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// causeOf renders an error for a recorded reason without leaking a secret.
func causeOf(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return strings.ReplaceAll(text, "\n", " ")
}

// nextAction tells an operator what the next step would be, which is what makes
// the resumed position legible without reading the event log (FR-023).
func nextAction(run domain.Run, phases []domain.PhaseState) string {
	switch run.Status {
	case domain.RunSucceeded:
		return "nothing; the run is complete"
	case domain.RunFailed:
		return "inspect the failed phase, then resume after fixing its cause"
	case domain.RunCancelled:
		return "nothing; the run was cancelled"
	}
	for _, phase := range phases {
		if phase.Status == domain.PhaseRunning {
			return "resume the run; phase " + phase.Name + " was interrupted"
		}
	}
	for _, phase := range phases {
		if !phase.Status.Terminal() {
			return "resume the run to execute phase " + phase.Name
		}
	}
	return "nothing; every phase reached a terminal state"
}
