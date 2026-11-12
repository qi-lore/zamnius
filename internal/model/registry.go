// Package model holds the provider-neutral model port and the registry that
// resolves a provider name to an adapter (contracts/model-client.md). It knows
// nothing about any specific provider: request and response shapes come from
// domain, and provider-specific mapping lives in adapter packages.
package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Client is the engine-facing model port. Go structural typing means any
// implementation of pkg/zamnius.ModelClient satisfies it without an import.
type Client interface {
	// Complete blocks until the model answers, the context ends, or an error
	// occurs. A returned error MUST be classified (contracts/model-client.md §4).
	Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error)
}

// Registry resolves provider names to clients.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]Client
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]Client)}
}

// Register binds a provider name to a client and returns the registry for
// chaining. Registering the same name twice replaces the earlier binding, which
// keeps a composition root free to override a default adapter.
func (r *Registry) Register(provider string, client Client) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[provider] = client
	return r
}

// Client returns the client registered for provider. An unknown provider is a
// permanent configuration failure whose hint names every known provider in
// sorted order, so a typo in a workflow definition is actionable (FR-052).
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
	sort.Strings(known)

	hint := "no providers are registered yet"
	if len(known) > 0 {
		hint = fmt.Sprintf("known providers: %s", strings.Join(known, ", "))
	}
	return nil, &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   fmt.Sprintf("no model client for provider %q", provider),
		Hint:  hint,
	}
}

// Classify maps err onto the closed ErrorClass set. An already classified error
// keeps its class; the remaining cases are the transport and context boundaries
// every adapter shares. A nil error is a bug in the caller and is reported as
// internal rather than as a zero-value class.
func Classify(err error) domain.ErrorClass {
	if err == nil {
		return domain.ErrClassInternal
	}
	var classified *domain.Error
	if errors.As(err, &classified) {
		return classified.Class
	}

	switch {
	case errors.Is(err, context.Canceled):
		return domain.ErrClassCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return domain.ErrClassTimeout
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return domain.ErrClassTimeout
		}
		return domain.ErrClassTransient
	}
	return domain.ErrClassInternal
}
