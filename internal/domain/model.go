package domain

import "encoding/json"

// Role is the author of a message handed to a model. Only user and assistant are
// modelled: the system prompt has its own field so adapters can map it per
// provider (contracts/model-client.md §1).
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one conversational turn.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// ModelRequest is the provider-neutral request shape (contracts/model-client.md §1).
type ModelRequest struct {
	Model           string
	System          string
	Messages        []Message
	MaxOutputTokens int
	Temperature     *float64
	ResponseSchema  json.RawMessage
	Metadata        map[string]string
}

// StopReason explains why generation stopped.
type StopReason string

// Stop reasons an adapter may report.
const (
	StopEnd           StopReason = "stop"
	StopLength        StopReason = "length"
	StopContentFilter StopReason = "content_filter"
	StopUnknown       StopReason = "unknown"
)

// Usage is token consumption reported by the provider. It is required: budgets
// depend on it, so a response without usage is invalid rather than zero-valued.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ModelResponse is the provider-neutral answer shape.
type ModelResponse struct {
	Content    string
	Usage      Usage
	StopReason StopReason
}
