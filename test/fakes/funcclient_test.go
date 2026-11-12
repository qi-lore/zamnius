package fakes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/test/fakes"
)

func TestFuncClientAnswersFromTheRequestAndRecordsCalls(t *testing.T) {
	t.Parallel()

	client := fakes.NewFuncClient(func(req domain.ModelRequest) (domain.ModelResponse, error) {
		return domain.ModelResponse{
			Content: "answer for " + req.Metadata["phase"],
			Usage:   domain.Usage{InputTokens: 3, OutputTokens: 1},
		}, nil
	})

	// Two concurrent branches are the reason this client exists: the answer must
	// follow the request, so the script cannot depend on call order.
	for _, phase := range []string{"alpha", "beta", "alpha"} {
		if _, err := client.Complete(context.Background(), domain.ModelRequest{
			Metadata: map[string]string{"phase": phase},
		}); err != nil {
			t.Fatalf("Complete(%s) error = %v", phase, err)
		}
	}

	if got := len(client.Calls()); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if got := client.CallCount("phase", "alpha"); got != 2 {
		t.Errorf("CallCount(phase, alpha) = %d, want 2", got)
	}
	if got := client.CallCount("phase", "gamma"); got != 0 {
		t.Errorf("CallCount(phase, gamma) = %d, want 0", got)
	}

	// Calls returns a copy: a test that sorts or truncates the slice must not be
	// able to corrupt the recorded history.
	calls := client.Calls()
	calls[0] = domain.ModelRequest{}
	if client.Calls()[0].Metadata["phase"] != "alpha" {
		t.Error("Calls() exposed the internal slice")
	}
}

func TestFuncClientSurfacesCancellationAndPropagatesErrors(t *testing.T) {
	t.Parallel()

	client := fakes.NewFuncClient(func(domain.ModelRequest) (domain.ModelResponse, error) {
		t.Error("the scripted function ran despite a cancelled context")
		return domain.ModelResponse{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var classified *domain.Error
	if _, err := client.Complete(ctx, domain.ModelRequest{Metadata: map[string]string{}}); !errors.As(err, &classified) {
		t.Fatalf("Complete() error = %v, want a *domain.Error", err)
	}
	if classified.Class != domain.ErrClassCancelled {
		t.Errorf("error class = %q, want cancelled", classified.Class)
	}
	if got := len(client.Calls()); got != 0 {
		t.Errorf("calls = %d, want 0: a refused call must not be recorded", got)
	}
}
