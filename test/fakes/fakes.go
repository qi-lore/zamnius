// Package fakes provides the deterministic test doubles the plan requires: a
// scripted model client, a fixed clock, a sequential identifier source, and a
// recording observer. Every recoverability and integration test runs offline
// because of these (research.md R10, FR-059).
package fakes

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-fu/zamnius/internal/domain"
)

// Step is one scripted model interaction: a response, an error, or both, with an
// optional delay so concurrency is observable in timestamps.
type Step struct {
	Response domain.ModelResponse
	Err      error
	Delay    time.Duration
}

// ModelClient replays a script and records what it was asked. It never touches
// the network, so tests are deterministic and offline.
type ModelClient struct {
	mu    sync.Mutex
	steps []Step
	calls []domain.ModelRequest
}

// NewModelClient builds a client that replays steps in order.
func NewModelClient(steps ...Step) *ModelClient {
	return &ModelClient{steps: steps}
}

// Complete implements the model contract by consuming the next scripted step.
func (c *ModelClient) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	c.mu.Lock()
	if len(c.steps) == 0 {
		c.mu.Unlock()
		// Exhausting the script is a test bug; surfacing it as a classified error
		// keeps the engine's error path exercised rather than hanging.
		return domain.ModelResponse{}, Error(domain.ErrClassInvalidResponse, "script exhausted")
	}
	step := c.steps[0]
	c.steps = c.steps[1:]
	c.calls = append(c.calls, req)
	c.mu.Unlock()

	if step.Delay > 0 {
		timer := time.NewTimer(step.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domain.ModelResponse{}, &domain.Error{
				Class: domain.ErrClassCancelled,
				Msg:   "cancelled during scripted delay",
				Err:   ctx.Err(),
			}
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassCancelled,
			Msg:   "cancelled before scripted response",
			Err:   err,
		}
	}
	if step.Err != nil {
		return domain.ModelResponse{}, step.Err
	}
	return step.Response, nil
}

// Calls returns the requests received so far, in order.
func (c *ModelClient) Calls() []domain.ModelRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.ModelRequest, len(c.calls))
	copy(out, c.calls)
	return out
}

// Remaining reports how many scripted steps are unspent.
func (c *ModelClient) Remaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.steps)
}

// Client is the model contract as the fakes see it. It is declared here rather
// than imported so the fakes stay independent of any layer; Go's structural
// typing makes it satisfy both the public contract and the engine's port.
type Client interface {
	Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error)
}

// Registry resolves provider names to registered clients.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]Client
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]Client)}
}

// Register binds a provider name to a client and returns the registry for chaining.
func (r *Registry) Register(provider string, client Client) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[provider] = client
	return r
}

// Client implements the model registry contract. The error names known providers
// so a typo in a definition is actionable rather than mysterious.
func (r *Registry) Client(provider string) (Client, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if client, ok := r.clients[provider]; ok {
		return client, nil
	}
	known := make([]string, 0, len(r.clients))
	for name := range r.clients {
		known = append(known, name)
	}
	return nil, &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   fmt.Sprintf("no model client for provider %q", provider),
		Hint:  fmt.Sprintf("known providers: %v", known),
	}
}

// Clock is a deterministic clock. Each Now call advances by Step so timestamps are
// strictly increasing, which keeps event ordering assertions meaningful.
type Clock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

// NewClock starts at start and advances by step on every read.
func NewClock(start time.Time, step time.Duration) *Clock {
	if step <= 0 {
		step = time.Millisecond
	}
	return &Clock{current: start.UTC(), step: step}
}

// Now returns the next deterministic instant.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.current
	c.current = c.current.Add(c.step)
	return now
}

// Peek returns the next instant without advancing, for assertions.
func (c *Clock) Peek() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// NewIDSource returns a sequential UUID generator, so a run's identifiers are
// reproducible across test executions.
func NewIDSource() func() uuid.UUID {
	var mu sync.Mutex
	counter := 0
	return func() uuid.UUID {
		mu.Lock()
		defer mu.Unlock()
		counter++
		return uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012x", counter))
	}
}

// PhaseFact records one observer callback about a phase.
type PhaseFact struct {
	Phase  string
	Status domain.PhaseStatus
}

// AttemptFact records one observer callback about an attempt.
type AttemptFact struct {
	Phase   string
	Attempt int
	Class   domain.ErrorClass
}

// Observer records engine facts in order, so tests can assert sequencing without
// reading the store.
type Observer struct {
	mu       sync.Mutex
	started  []string
	attempts []AttemptFact
	finished []PhaseFact
}

// NewObserver builds an empty recording observer.
func NewObserver() *Observer { return &Observer{} }

// OnPhaseStarted implements the observer contract.
func (o *Observer) OnPhaseStarted(_ context.Context, _ uuid.UUID, phase string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, phase)
}

// OnAttemptFinished implements the observer contract.
func (o *Observer) OnAttemptFinished(_ context.Context, _ uuid.UUID, phase string, attempt int, class domain.ErrorClass) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.attempts = append(o.attempts, AttemptFact{Phase: phase, Attempt: attempt, Class: class})
}

// OnPhaseFinished implements the observer contract.
func (o *Observer) OnPhaseFinished(_ context.Context, _ uuid.UUID, phase string, status domain.PhaseStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, PhaseFact{Phase: phase, Status: status})
}

// Started returns the phases reported as started, in order.
func (o *Observer) Started() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, len(o.started))
	copy(out, o.started)
	return out
}

// Attempts returns attempt facts in order.
func (o *Observer) Attempts() []AttemptFact {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]AttemptFact, len(o.attempts))
	copy(out, o.attempts)
	return out
}

// Finished returns phase facts in order.
func (o *Observer) Finished() []PhaseFact {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]PhaseFact, len(o.finished))
	copy(out, o.finished)
	return out
}

// Error builds a classified error, keeping test scripts honest about class.
func Error(class domain.ErrorClass, message string) error {
	return &domain.Error{Class: class, Msg: message}
}

// Text is a convenient successful response with token usage.
func Text(content string, inputTokens, outputTokens int) Step {
	return Step{Response: domain.ModelResponse{
		Content:    content,
		Usage:      domain.Usage{InputTokens: inputTokens, OutputTokens: outputTokens},
		StopReason: domain.StopEnd,
	}}
}
