package model_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/model"
)

// stubClient is a named client so tests can prove which registration won.
type stubClient struct {
	name string
}

func (s *stubClient) Complete(context.Context, domain.ModelRequest) (domain.ModelResponse, error) {
	return domain.ModelResponse{Content: s.name}, nil
}

func TestRegistryUnknownProviderNamesKnownProviders(t *testing.T) {
	t.Parallel()

	registry := model.NewRegistry().
		Register("zeta", &stubClient{name: "zeta"}).
		Register("alpha", &stubClient{name: "alpha"}).
		Register("middle", &stubClient{name: "middle"})

	client, err := registry.Client("typo")
	if client != nil {
		t.Fatalf("Client(typo) = %v, want nil client", client)
	}
	if err == nil {
		t.Fatal("Client(typo) returned no error")
	}
	if got := domain.ClassOf(err); got != domain.ErrClassPermanent {
		t.Errorf("ClassOf(err) = %q, want %q", got, domain.ErrClassPermanent)
	}
	var classified *domain.Error
	if !errors.As(err, &classified) {
		t.Fatalf("err = %v, want *domain.Error", err)
	}
	if want := "known providers: alpha, middle, zeta"; classified.Hint != want {
		t.Errorf("Hint = %q, want %q", classified.Hint, want)
	}
	if classified.Msg != `no model client for provider "typo"` {
		t.Errorf("Msg = %q, want it to name the provider", classified.Msg)
	}
}

func TestRegistryEmptyHintSaysNoProviders(t *testing.T) {
	t.Parallel()

	_, err := model.NewRegistry().Client("anything")
	if err == nil {
		t.Fatal("Client on an empty registry returned no error")
	}
	if hint := domain.HintOf(err); hint != "no providers are registered yet" {
		t.Errorf("Hint = %q, want the empty-registry hint", hint)
	}
}

func TestRegistryRegistrationOrderDoesNotAffectResolution(t *testing.T) {
	t.Parallel()

	first := &stubClient{name: "first"}
	second := &stubClient{name: "second"}

	forward := model.NewRegistry().Register("one", first).Register("two", second)
	reverse := model.NewRegistry().Register("two", second).Register("one", first)

	for provider, want := range map[string]*stubClient{"one": first, "two": second} {
		for name, registry := range map[string]*model.Registry{"forward": forward, "reverse": reverse} {
			got, err := registry.Client(provider)
			if err != nil {
				t.Fatalf("%s.Client(%q) error = %v", name, provider, err)
			}
			if got != want {
				t.Errorf("%s.Client(%q) = %v, want %v", name, provider, got, want)
			}
		}
	}
}

func TestRegistryRegisterReplacesExistingBinding(t *testing.T) {
	t.Parallel()

	replacement := &stubClient{name: "replacement"}
	registry := model.NewRegistry().
		Register("provider", &stubClient{name: "original"}).
		Register("provider", replacement)

	got, err := registry.Client("provider")
	if err != nil {
		t.Fatalf("Client(provider) error = %v", err)
	}
	if got != replacement {
		t.Errorf("Client(provider) = %v, want the later registration", got)
	}
}

// timeoutError is a net.Error whose timeout flag is controlled by the test.
type timeoutError struct {
	timeout bool
}

func (e timeoutError) Error() string   { return "network failure" }
func (e timeoutError) Timeout() bool   { return e.timeout }
func (e timeoutError) Temporary() bool { return e.timeout }

func TestClassify(t *testing.T) {
	t.Parallel()

	classified := &domain.Error{Class: domain.ErrClassRateLimited, Msg: "slow down"}
	explicitInternal := &domain.Error{Class: domain.ErrClassInternal, Msg: "bug"}
	classifiedOverCancel := &domain.Error{Class: domain.ErrClassPermanent, Msg: "bad request", Err: context.Canceled}

	tests := []struct {
		name string
		err  error
		want domain.ErrorClass
	}{
		{"classified", classified, domain.ErrClassRateLimited},
		{"classified wrapped", fmt.Errorf("attempt failed: %w", classified), domain.ErrClassRateLimited},
		{"classified internal is preserved", explicitInternal, domain.ErrClassInternal},
		{"classified wins over its context cause", classifiedOverCancel, domain.ErrClassPermanent},
		{"context canceled", context.Canceled, domain.ErrClassCancelled},
		{"wrapped context canceled", fmt.Errorf("call: %w", context.Canceled), domain.ErrClassCancelled},
		{"context deadline", context.DeadlineExceeded, domain.ErrClassTimeout},
		{"wrapped context deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), domain.ErrClassTimeout},
		{"net error timeout", timeoutError{timeout: true}, domain.ErrClassTimeout},
		{"net error not timeout", timeoutError{timeout: false}, domain.ErrClassTransient},
		{"wrapped net error", fmt.Errorf("dial: %w", timeoutError{timeout: false}), domain.ErrClassTransient},
		{"unclassified", errors.New("mystery"), domain.ErrClassInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := model.Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyNilIsInternal(t *testing.T) {
	t.Parallel()

	if got := model.Classify(nil); got != domain.ErrClassInternal {
		t.Errorf("Classify(nil) = %q, want %q", got, domain.ErrClassInternal)
	}
}

// Ensure the timeout error really is a net.Error, so the table above exercises
// the interface branch rather than a plain error.
var _ net.Error = timeoutError{}
