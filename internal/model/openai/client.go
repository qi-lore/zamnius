// Package openai adapts the provider-neutral model port to the OpenAI-compatible
// Chat Completions API (contracts/model-client.md §3). It owns every
// provider-specific request and response shape in the project; nothing above it
// may know about JSON field names.
//
// The adapter adds no timeout and no hidden retry: the caller's context governs
// wall-clock and the engine owns retry policy. The single exception is the
// contract-mandated structured-output fallback, which retries exactly once when
// the server rejects response_format by name.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/model"
	"github.com/qi-lore/zamnius/internal/observability"
)

const (
	// defaultBaseURL is the provider root used when Options.BaseURL is empty.
	defaultBaseURL = "https://api.openai.com/v1"
	// defaultSchemaName is the structured-output name when no phase is supplied.
	defaultSchemaName = "response"
	// maxSchemaNameLen is the JSON Schema name limit enforced by the provider.
	maxSchemaNameLen = 64
	// bodyExcerptLimit bounds how much provider text reaches an error hint.
	bodyExcerptLimit = 256
)

// schemaNameUnsafe matches every character the provider disallows in a JSON
// Schema name. Each match is replaced one-for-one, so the result stays readable.
var schemaNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// Client is the OpenAI-compatible implementation of the model port.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
	logger   *slog.Logger
}

// Client must satisfy the engine-facing port without importing it at the call site.
var _ model.Client = (*Client)(nil)

// Options configures a Client. Every zero value has a documented default.
type Options struct {
	// BaseURL is the provider root, e.g. "https://api.openai.com/v1".
	BaseURL string
	// APIKey authenticates requests. It is read from the environment when empty
	// and is never logged, recorded, or written into an error message.
	APIKey string
	// HTTP is the transport. It is used as-is; no timeout is added, because the
	// caller's context governs the call.
	HTTP *http.Client
	// Logger receives one Debug line per call. The default discards output.
	Logger *slog.Logger
}

// New builds a client. A missing key or an unusable base URL is refused here so
// the composition root fails while it is still assembling configuration, rather
// than after a phase has started.
func New(opts Options) (*Client, error) {
	rawBase := strings.TrimSpace(opts.BaseURL)
	if rawBase == "" {
		rawBase = defaultBaseURL
	}
	endpoint := strings.TrimRight(rawBase, "/") + "/chat/completions"
	if !validEndpoint(endpoint) {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   fmt.Sprintf("openai: base URL %q is not an absolute http(s) URL", rawBase),
			Hint:  "set base_url to the provider root, for example https://api.openai.com/v1",
		}
	}

	key := opts.APIKey
	if key == "" {
		key = os.Getenv(observability.SecretEnvVar)
	}
	if key == "" {
		return nil, missingKeyError()
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Client{
		endpoint: endpoint,
		apiKey:   key,
		http:     httpClient,
		logger:   logger,
	}, nil
}

// Complete implements model.Client. It never retries a transport or provider
// failure; the only repeat is the structured-output fallback in complete.
func (c *Client) Complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	start := time.Now()
	resp, err := c.complete(ctx, req)
	c.logCall(ctx, req, resp, err, time.Since(start))
	return resp, err
}

func (c *Client) complete(ctx context.Context, req domain.ModelRequest) (domain.ModelResponse, error) {
	if c.apiKey == "" {
		return domain.ModelResponse{}, missingKeyError()
	}
	if req.MaxOutputTokens <= 0 {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "openai: max_output_tokens must be positive",
			Hint:  "set max_output_tokens above zero in the workflow definition",
		}
	}

	structured := len(req.ResponseSchema) > 0
	name := schemaName(req)

	payload, err := c.buildPayload(req, name, false)
	if err != nil {
		return domain.ModelResponse{}, err
	}
	status, body, err := c.post(ctx, payload)
	if err != nil {
		return domain.ModelResponse{}, err
	}

	// The one sanctioned repeat: a server that rejects response_format by name
	// gets a single json_object attempt with local validation.
	if structured && status == http.StatusBadRequest && mentionsResponseFormat(body) {
		fallback, buildErr := c.buildPayload(req, name, true)
		if buildErr != nil {
			return domain.ModelResponse{}, buildErr
		}
		status, body, err = c.post(ctx, fallback)
		if err != nil {
			return domain.ModelResponse{}, err
		}
		if !successful(status) {
			return domain.ModelResponse{}, c.statusError(status, body)
		}
		resp, parseErr := parseResponse(body)
		if parseErr != nil {
			return domain.ModelResponse{}, parseErr
		}
		if !json.Valid([]byte(resp.Content)) {
			return domain.ModelResponse{}, invalidStructuredError()
		}
		return resp, nil
	}

	if !successful(status) {
		return domain.ModelResponse{}, c.statusError(status, body)
	}
	resp, err := parseResponse(body)
	if err != nil {
		return domain.ModelResponse{}, err
	}
	if structured && !json.Valid([]byte(resp.Content)) {
		return domain.ModelResponse{}, invalidStructuredError()
	}
	return resp, nil
}

// successful reports whether a status carries a parseable answer. 2xx is the
// provider's success range; a 204 still fails later because usage is absent.
func successful(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

// post sends one request and returns the status and body. The close error is
// intentionally dropped: the response is fully read before it is closed, so a
// close failure cannot lose an answer.
func (c *Client) post(ctx context.Context, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, c.transportError(ctx, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, c.transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, c.transportError(ctx, err)
	}
	return resp.StatusCode, body, nil
}

// requestBody mirrors the Chat Completions request. Temperature is a pointer so
// a nil value disappears from the wire and the provider default applies.
type requestBody struct {
	Model          string           `json:"model"`
	Messages       []requestMessage `json:"messages"`
	MaxTokens      int              `json:"max_tokens"`
	Temperature    *float64         `json:"temperature,omitempty"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
}

type requestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

func (c *Client) buildPayload(req domain.ModelRequest, name string, fallback bool) ([]byte, error) {
	messages := make([]requestMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, requestMessage{Role: "system", Content: req.System})
	}
	for _, message := range req.Messages {
		messages = append(messages, requestMessage{Role: string(message.Role), Content: message.Content})
	}

	body := requestBody{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
	}
	if len(req.ResponseSchema) > 0 {
		if !json.Valid(req.ResponseSchema) {
			return nil, &domain.Error{
				Class: domain.ErrClassPermanent,
				Msg:   "openai: response schema is not valid JSON",
				Hint:  "supply a JSON Schema document in response_schema",
			}
		}
		if fallback {
			body.ResponseFormat = &responseFormat{Type: "json_object"}
		} else {
			body.ResponseFormat = &responseFormat{
				Type: "json_schema",
				JSONSchema: &jsonSchema{
					Name:   name,
					Schema: req.ResponseSchema,
					Strict: true,
				},
			}
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "openai: request cannot be encoded as JSON",
			Hint:  "check that temperature is a finite number and the response schema is valid",
			Err:   err,
		}
	}
	return payload, nil
}

// chatResponse mirrors only the fields this increment consumes.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func parseResponse(body []byte) (domain.ModelResponse, error) {
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassInvalidResponse,
			Msg:   "openai: response body is not valid JSON",
			Hint:  "check that base_url points at an OpenAI-compatible chat completions endpoint",
			Err:   err,
		}
	}
	if len(decoded.Choices) == 0 {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassInvalidResponse,
			Msg:   "openai: response has no choices",
			Hint:  "the provider must return at least one choice entry",
		}
	}
	if decoded.Usage == nil {
		return domain.ModelResponse{}, &domain.Error{
			Class: domain.ErrClassInvalidResponse,
			Msg:   "openai: response has no usage object",
			Hint:  "token budgets depend on usage, so an answer without it is rejected rather than estimated",
		}
	}

	choice := decoded.Choices[0]
	return domain.ModelResponse{
		Content:    choice.Message.Content,
		Usage:      domain.Usage{InputTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens},
		StopReason: mapStopReason(choice.FinishReason),
	}, nil
}

func mapStopReason(reason string) domain.StopReason {
	switch reason {
	case "stop":
		return domain.StopEnd
	case "length":
		return domain.StopLength
	case "content_filter":
		return domain.StopContentFilter
	default:
		return domain.StopUnknown
	}
}

// statusError classifies a non-2xx answer. The message carries the status and the
// hint carries a short, redacted body excerpt so an operator can act on it.
func (c *Client) statusError(status int, body []byte) error {
	excerpt := c.excerpt(body)
	hint := excerpt
	if hint == "" {
		hint = "the provider returned no body"
	}
	return &domain.Error{
		Class: classifyStatus(status, string(body)),
		Msg:   fmt.Sprintf("openai: HTTP %d", status),
		Hint:  hint,
	}
}

func classifyStatus(status int, body string) domain.ErrorClass {
	switch status {
	case http.StatusTooManyRequests:
		return domain.ErrClassRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		return domain.ErrClassAuth
	case http.StatusRequestTimeout:
		return domain.ErrClassTimeout
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		if mentionsContextOverflow(body) {
			return domain.ErrClassContextOverflow
		}
		return domain.ErrClassPermanent
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return domain.ErrClassTransient
	default:
		return domain.ErrClassInternal
	}
}

func mentionsResponseFormat(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "response_format") || strings.Contains(lower, "response format")
}

func mentionsContextOverflow(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{"context_length_exceeded", "maximum context length", "context length", "context window"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// transportError maps a transport or context failure onto the closed class set.
// The context is consulted as well, because a wrapped cancellation is not always
// the value the transport returns.
func (c *Client) transportError(ctx context.Context, err error) error {
	class := model.Classify(err)
	if ctxErr := ctx.Err(); ctxErr != nil {
		class = model.Classify(ctxErr)
	}

	msg := "openai: request failed"
	hint := "check network connectivity and the configured base URL"
	switch class {
	case domain.ErrClassCancelled:
		msg = "openai: call cancelled"
		hint = "the caller cancelled the call; the phase ends as cancelled"
	case domain.ErrClassTimeout:
		msg = "openai: call timed out"
		hint = "the caller's deadline elapsed; the engine decides whether a retry is allowed"
	case domain.ErrClassTransient:
		hint = "transient transport failure; the engine may retry according to the phase policy"
	}
	return &domain.Error{Class: class, Msg: msg, Hint: hint, Err: err}
}

func invalidStructuredError() error {
	return &domain.Error{
		Class: domain.ErrClassInvalidResponse,
		Msg:   "openai: structured response is not valid JSON",
		Hint:  "the answer must be a JSON document because response_schema was set",
	}
}

func missingKeyError() error {
	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "openai: no API key is configured",
		Hint:  fmt.Sprintf("set %s in the environment; the key is never read from a workflow definition", observability.SecretEnvVar),
	}
}

// excerpt collapses provider text to one line, redacts secrets, and bounds it.
func (c *Client) excerpt(body []byte) string {
	text := strings.Join(strings.Fields(c.redact(string(body))), " ")
	runes := []rune(text)
	if len(runes) > bodyExcerptLimit {
		text = string(runes[:bodyExcerptLimit]) + "..."
	}
	return text
}

// redact scrubs the environment secrets and this client's own key.
func (c *Client) redact(value string) string {
	secrets := observability.SecretValues()
	if c.apiKey != "" {
		secrets = append(secrets, c.apiKey)
	}
	return observability.Redact(value, secrets)
}

// logCall writes one Debug line per call. Headers and bodies never reach it:
// only the model, token counts, duration, and outcome class do.
func (c *Client) logCall(ctx context.Context, req domain.ModelRequest, resp domain.ModelResponse, err error, elapsed time.Duration) {
	if c.logger == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = string(model.Classify(err))
	}
	c.logger.LogAttrs(ctx, slog.LevelDebug, "model call",
		slog.String("model", c.redact(req.Model)),
		slog.Int("input_tokens", resp.Usage.InputTokens),
		slog.Int("output_tokens", resp.Usage.OutputTokens),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.String("outcome", outcome),
	)
}

// schemaName derives a provider-safe JSON Schema name from the phase metadata.
func schemaName(req domain.ModelRequest) string {
	name := schemaNameUnsafe.ReplaceAllString(req.Metadata["phase"], "_")
	if name == "" {
		name = defaultSchemaName
	}
	if len(name) > maxSchemaNameLen {
		name = name[:maxSchemaNameLen]
	}
	return name
}

// validEndpoint reports whether endpoint is an absolute http(s) URL.
func validEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}
