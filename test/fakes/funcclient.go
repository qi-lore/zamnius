package fakes

import (
	"context"
	"sync"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

// FuncClient answers by inspecting the request instead of replaying a queue. A
// fan-out phase calls several branches concurrently, so a queue would make the
// script depend on which branch happened to call first; a function keeps the test
// deterministic and lets it key behaviour on the phase or branch name carried in
// the request metadata.
type FuncClient struct {
	mu    sync.Mutex
	fn    func(domain.ModelRequest) (domain.ModelResponse, error)
	calls []domain.ModelRequest
}

// NewFuncClient builds a client that derives every answer from the request.
func NewFuncClient(fn func(domain.ModelRequest) (domain.ModelResponse, error)) *FuncClient {
	return &FuncClient{fn: fn}
}

// Complete implements the model contract.
func (c *FuncClient) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassCancelled,
			Msg:   "cancelled before scripted response",
			Err:   err,
		}
	}
	c.mu.Lock()
	c.calls = append(c.calls, req)
	c.mu.Unlock()
	return c.fn(req)
}

// Calls returns every request received so far, in call order.
func (c *FuncClient) Calls() []domain.ModelRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.ModelRequest, len(c.calls))
	copy(out, c.calls)
	return out
}

// CallCount reports how many requests carried the given metadata value for key.
func (c *FuncClient) CallCount(key, value string) int {
	count := 0
	for _, call := range c.Calls() {
		if call.Metadata[key] == value {
			count++
		}
	}
	return count
}
