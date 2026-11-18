package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/model/openai"
	"github.com/qi-lore/zamnius/internal/observability"
)

// canaryKey stands in for a real credential. Every test that captures logs or
// error text asserts this value never appears (contracts/model-client.md §6.6).
const canaryKey = "sk-canary-do-not-leak-5f3a"

var schemaNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func newTestClient(t *testing.T, baseURL string) *openai.Client {
	t.Helper()
	client, err := openai.New(openai.Options{BaseURL: baseURL, APIKey: canaryKey})
	if err != nil {
		t.Fatalf("openai.New() error = %v", err)
	}
	return client
}

func startServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func testRequest() domain.ModelRequest {
	return domain.ModelRequest{
		Model:           "gpt-test",
		System:          "You are terse.",
		Messages:        []domain.Message{{Role: domain.RoleUser, Content: "hello"}, {Role: domain.RoleAssistant, Content: "hi"}},
		MaxOutputTokens: 128,
		Metadata:        map[string]string{"phase": "draft"},
	}
}

func writeChatResponse(w http.ResponseWriter, content, finish string, inputTokens, outputTokens int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w,
		`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":%q}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
		content, finish, inputTokens, outputTokens)
}

func writeError(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func requireClass(t *testing.T, err error, want domain.ErrorClass) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q", want)
	}
	var classified *domain.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error %v is not a *domain.Error", err)
	}
	if classified.Class != want {
		t.Errorf("error class = %q, want %q (error: %v)", classified.Class, want, err)
	}
	return classified
}

// capturedRequest is what the fake provider saw.
type capturedRequest struct {
	Method        string
	Path          string
	ContentType   string
	Authorization string
	Body          map[string]any
}

func TestCompleteSuccessSendsMappedRequest(t *testing.T) {
	t.Parallel()

	captured := make(chan capturedRequest, 1)
	server := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		request := capturedRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			ContentType:   r.Header.Get("Content-Type"),
			Authorization: r.Header.Get("Authorization"),
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if err := json.Unmarshal(body, &request.Body); err != nil {
			t.Errorf("decode request body %q: %v", body, err)
			return
		}
		captured <- request
		writeChatResponse(w, "hello back", "stop", 11, 7)
	})

	client := newTestClient(t, server.URL)
	resp, err := client.Complete(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "hello back" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello back")
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Errorf("Usage = %+v, want {InputTokens:11 OutputTokens:7}", resp.Usage)
	}
	if resp.StopReason != domain.StopEnd {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, domain.StopEnd)
	}

	request := <-captured
	if request.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", request.Method)
	}
	if request.Path != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", request.Path)
	}
	if request.ContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", request.ContentType)
	}
	if request.Authorization != "Bearer "+canaryKey {
		t.Errorf("Authorization = %q, want a bearer token", request.Authorization)
	}
	if got := request.Body["model"]; got != "gpt-test" {
		t.Errorf("model = %v, want gpt-test", got)
	}
	if got := request.Body["max_tokens"]; got != float64(128) {
		t.Errorf("max_tokens = %v, want 128", got)
	}
	if _, present := request.Body["temperature"]; present {
		t.Errorf("temperature is present with a nil request value, want it omitted")
	}
	if _, present := request.Body["response_format"]; present {
		t.Errorf("response_format is present without a schema, want it omitted")
	}

	messages, ok := request.Body["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %v, want an array", request.Body["messages"])
	}
	want := []domain.Message{
		{Role: "system", Content: "You are terse."},
		{Role: domain.RoleUser, Content: "hello"},
		{Role: domain.RoleAssistant, Content: "hi"},
	}
	if len(messages) != len(want) {
		t.Fatalf("len(messages) = %d, want %d (%v)", len(messages), len(want), messages)
	}
	for i, expected := range want {
		entry, ok := messages[i].(map[string]any)
		if !ok {
			t.Fatalf("messages[%d] = %v, want an object", i, messages[i])
		}
		if entry["role"] != string(expected.Role) || entry["content"] != expected.Content {
			t.Errorf("messages[%d] = %v, want role %q content %q", i, entry, expected.Role, expected.Content)
		}
	}
}

func TestCompletePassesTemperatureWhenSet(t *testing.T) {
	t.Parallel()

	captured := make(chan map[string]any, 1)
	server := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		captured <- body
		writeChatResponse(w, "ok", "stop", 1, 1)
	})

	req := testRequest()
	temperature := 0.25
	req.Temperature = &temperature

	if _, err := newTestClient(t, server.URL).Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got := (<-captured)["temperature"]; got != 0.25 {
		t.Errorf("temperature = %v, want 0.25", got)
	}
}

func TestCompleteMapsFinishReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		finish string
		want   domain.StopReason
	}{
		{"stop", domain.StopEnd},
		{"length", domain.StopLength},
		{"content_filter", domain.StopContentFilter},
		{"tool_calls", domain.StopUnknown},
		{"", domain.StopUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.finish, func(t *testing.T) {
			t.Parallel()
			server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeChatResponse(w, "content", tt.finish, 3, 4)
			})
			resp, err := newTestClient(t, server.URL).Complete(context.Background(), testRequest())
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if resp.StopReason != tt.want {
				t.Errorf("finish_reason %q maps to %q, want %q", tt.finish, resp.StopReason, tt.want)
			}
		})
	}
}

func TestCompleteClassifiesHTTPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   domain.ErrorClass
	}{
		{"rate limited", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, domain.ErrClassRateLimited},
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"bad key ` + canaryKey + `"}}`, domain.ErrClassAuth},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"forbidden"}}`, domain.ErrClassAuth},
		{"internal server error", http.StatusInternalServerError, `{"error":{"message":"boom"}}`, domain.ErrClassTransient},
		{"bad gateway", http.StatusBadGateway, `{"error":{"message":"gateway"}}`, domain.ErrClassTransient},
		{"unavailable", http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`, domain.ErrClassTransient},
		{"gateway timeout", http.StatusGatewayTimeout, `{"error":{"message":"timeout"}}`, domain.ErrClassTransient},
		{"bad request", http.StatusBadRequest, `{"error":{"message":"malformed"}}`, domain.ErrClassPermanent},
		{"not found", http.StatusNotFound, `{"error":{"message":"no model"}}`, domain.ErrClassPermanent},
		{"unprocessable", http.StatusUnprocessableEntity, `{"error":{"message":"unprocessable"}}`, domain.ErrClassPermanent},
		{"request timeout", http.StatusRequestTimeout, `{"error":{"message":"slow"}}`, domain.ErrClassTimeout},
		{"unexpected", http.StatusTeapot, `{"error":{"message":"teapot"}}`, domain.ErrClassInternal},
		{"context overflow", http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`, domain.ErrClassContextOverflow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, tt.status, tt.body)
			})
			_, err := newTestClient(t, server.URL).Complete(context.Background(), testRequest())
			classified := requireClass(t, err, tt.want)
			if !strings.Contains(classified.Msg, strconv.Itoa(tt.status)) {
				t.Errorf("Msg = %q, want it to name HTTP %d", classified.Msg, tt.status)
			}
			if classified.Hint == "" {
				t.Error("Hint is empty, want a body excerpt")
			}
			if strings.Contains(err.Error(), canaryKey) || strings.Contains(classified.Hint, canaryKey) {
				t.Errorf("error text leaked the API key: %v", err)
			}
		})
	}
}

func TestCompleteRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"malformed", `{"choices":[`},
		{"missing usage", `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`},
		{"null usage", `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":null}`},
		{"no choices", `{"usage":{"prompt_tokens":1,"completion_tokens":2}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			})
			resp, err := newTestClient(t, server.URL).Complete(context.Background(), testRequest())
			if resp != (domain.ModelResponse{}) {
				t.Errorf("response = %+v, want the zero value", resp)
			}
			requireClass(t, err, domain.ErrClassInvalidResponse)
		})
	}
}

// wireFormat is the captured response_format object.
type wireFormat struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict bool            `json:"strict"`
	} `json:"json_schema"`
}

type wireRequest struct {
	Model          string      `json:"model"`
	Messages       []wireEntry `json:"messages"`
	MaxTokens      int         `json:"max_tokens"`
	Temperature    *float64    `json:"temperature"`
	ResponseFormat *wireFormat `json:"response_format"`
}

type wireEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func TestCompleteSchemaFallbackRetriesOnceAndValidates(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`)
	captured := make(chan wireRequest, 2)
	var calls atomic.Int32

	server := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request wireRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		captured <- request
		if calls.Add(1) == 1 {
			writeError(w, http.StatusBadRequest, `{"error":{"message":"Invalid parameter: response_format is not supported"}}`)
			return
		}
		writeChatResponse(w, `{"answer":42}`, "stop", 21, 5)
	})

	req := testRequest()
	req.ResponseSchema = schema
	req.Metadata = map[string]string{"phase": "phase/one ünicode"}

	resp, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != `{"answer":42}` {
		t.Errorf("Content = %q, want %q", resp.Content, `{"answer":42}`)
	}
	if resp.Usage.InputTokens != 21 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want {InputTokens:21 OutputTokens:5}", resp.Usage)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("HTTP calls = %d, want exactly 2 (initial + one fallback)", got)
	}
	if len(captured) != 2 {
		t.Fatalf("captured requests = %d, want 2", len(captured))
	}

	first := <-captured
	if first.ResponseFormat == nil || first.ResponseFormat.Type != "json_schema" {
		t.Fatalf("first response_format = %+v, want type json_schema", first.ResponseFormat)
	}
	if first.ResponseFormat.JSONSchema == nil {
		t.Fatal("first response_format has no json_schema object")
	}
	if !first.ResponseFormat.JSONSchema.Strict {
		t.Error("json_schema.strict = false, want true")
	}
	if name := first.ResponseFormat.JSONSchema.Name; !schemaNamePattern.MatchString(name) {
		t.Errorf("json_schema.name = %q, want it to match %s", name, schemaNamePattern)
	}
	if name := first.ResponseFormat.JSONSchema.Name; name != "phase_one__nicode" {
		t.Errorf("json_schema.name = %q, want sanitised %q", name, "phase_one__nicode")
	}
	var gotSchema, wantSchema any
	if err := json.Unmarshal(first.ResponseFormat.JSONSchema.Schema, &gotSchema); err != nil {
		t.Errorf("sent schema is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(schema, &wantSchema); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if fmt.Sprint(gotSchema) != fmt.Sprint(wantSchema) {
		t.Errorf("sent schema = %v, want %v", gotSchema, wantSchema)
	}

	second := <-captured
	if second.ResponseFormat == nil || second.ResponseFormat.Type != "json_object" {
		t.Fatalf("fallback response_format = %+v, want type json_object", second.ResponseFormat)
	}
	if second.ResponseFormat.JSONSchema != nil {
		t.Errorf("fallback has a json_schema object, want only {\"type\":\"json_object\"}")
	}
	if second.MaxTokens != req.MaxOutputTokens {
		t.Errorf("fallback max_tokens = %d, want %d", second.MaxTokens, req.MaxOutputTokens)
	}
}

func TestCompleteSchemaFallbackInvalidJSONIsInvalidResponse(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeError(w, http.StatusBadRequest, `{"error":{"message":"response_format unsupported"}}`)
			return
		}
		writeChatResponse(w, "not-json", "stop", 3, 2)
	})

	req := testRequest()
	req.ResponseSchema = json.RawMessage(`{"type":"object"}`)

	resp, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	if resp != (domain.ModelResponse{}) {
		t.Errorf("response = %+v, want the zero value", resp)
	}
	requireClass(t, err, domain.ErrClassInvalidResponse)
	if got := calls.Load(); got != 2 {
		t.Errorf("HTTP calls = %d, want 2", got)
	}
}

func TestCompleteSchemaSuccessStillValidatesLocally(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeChatResponse(w, "plain text", "stop", 3, 2)
	})

	req := testRequest()
	req.ResponseSchema = json.RawMessage(`{"type":"object"}`)

	_, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	requireClass(t, err, domain.ErrClassInvalidResponse)
	if got := calls.Load(); got != 1 {
		t.Errorf("HTTP calls = %d, want 1 (no retry for a 200 answer)", got)
	}
}

func TestCompleteNoFallbackWhenErrorDoesNotNameResponseFormat(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeError(w, http.StatusBadRequest, `{"error":{"message":"model does not exist"}}`)
	})

	req := testRequest()
	req.ResponseSchema = json.RawMessage(`{"type":"object"}`)

	_, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	requireClass(t, err, domain.ErrClassPermanent)
	if got := calls.Load(); got != 1 {
		t.Errorf("HTTP calls = %d, want 1 (fallback is only for response_format rejection)", got)
	}
}

func TestCompleteCancellationReturnsCancelled(t *testing.T) {
	t.Parallel()

	server := startServer(t, func(_ http.ResponseWriter, r *http.Request) {
		// The body must be drained first: net/http only starts the background read
		// that notices a client disconnect once the request body reaches EOF, and
		// Close would otherwise block on this handler forever.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := newTestClient(t, server.URL).Complete(ctx, testRequest())
	elapsed := time.Since(start)

	requireClass(t, err, domain.ErrClassCancelled)
	if elapsed > time.Second {
		t.Errorf("cancellation took %v, want it to return within a short grace period", elapsed)
	}
}

func TestCompleteDeadlineReturnsTimeout(t *testing.T) {
	t.Parallel()

	server := startServer(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := newTestClient(t, server.URL).Complete(ctx, testRequest())
	elapsed := time.Since(start)

	requireClass(t, err, domain.ErrClassTimeout)
	if elapsed > time.Second {
		t.Errorf("deadline took %v, want it to return within a short grace period", elapsed)
	}
}

func TestCompleteMissingAPIKeyRefusedWithoutHTTPCall(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv(observability.SecretEnvVar, "")

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeChatResponse(w, "should not happen", "stop", 1, 1)
	})

	client, err := openai.New(openai.Options{BaseURL: server.URL})
	if client != nil {
		t.Errorf("New() returned a client, want nil when the key is missing")
	}
	classified := requireClass(t, err, domain.ErrClassPermanent)
	if !strings.Contains(classified.Hint, observability.SecretEnvVar) {
		t.Errorf("Hint = %q, want it to name %s", classified.Hint, observability.SecretEnvVar)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("HTTP calls = %d, want 0 (an unauthenticated request must never be sent)", got)
	}
}

func TestCompleteRejectsNonPositiveMaxTokensWithoutHTTPCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeChatResponse(w, "should not happen", "stop", 1, 1)
	})

	req := testRequest()
	req.MaxOutputTokens = 0

	_, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	classified := requireClass(t, err, domain.ErrClassPermanent)
	if !strings.Contains(classified.Hint, "max_output_tokens") {
		t.Errorf("Hint = %q, want it to name max_output_tokens", classified.Hint)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("HTTP calls = %d, want 0", got)
	}
}

func TestCompleteRejectsInvalidSchemaWithoutHTTPCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeChatResponse(w, "should not happen", "stop", 1, 1)
	})

	req := testRequest()
	req.ResponseSchema = json.RawMessage(`{"type":`)

	_, err := newTestClient(t, server.URL).Complete(context.Background(), req)
	requireClass(t, err, domain.ErrClassPermanent)
	if got := calls.Load(); got != 0 {
		t.Errorf("HTTP calls = %d, want 0", got)
	}
}

func TestLogsNeverContainSecret(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeChatResponse(w, "fine", "stop", 9, 4)
		})
		client, err := openai.New(openai.Options{BaseURL: server.URL, APIKey: canaryKey, Logger: logger})
		if err != nil {
			t.Fatalf("openai.New() error = %v", err)
		}
		if _, err := client.Complete(context.Background(), testRequest()); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}

		output := logs.String()
		if output == "" {
			t.Fatal("no log line was written, want one Debug line per call")
		}
		for _, forbidden := range []string{canaryKey, "Authorization", "Bearer"} {
			if strings.Contains(output, forbidden) {
				t.Errorf("log output contains %q: %s", forbidden, output)
			}
		}
		for _, required := range []string{`"model":"gpt-test"`, `"input_tokens":9`, `"output_tokens":4`, `"outcome":"ok"`, `"duration_ms":`} {
			if !strings.Contains(output, required) {
				t.Errorf("log output is missing %s: %s", required, output)
			}
		}
	})

	t.Run("auth failure", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		server := startServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusUnauthorized, `{"error":{"message":"invalid key `+canaryKey+`"}}`)
		})
		client, err := openai.New(openai.Options{BaseURL: server.URL, APIKey: canaryKey, Logger: logger})
		if err != nil {
			t.Fatalf("openai.New() error = %v", err)
		}
		_, callErr := client.Complete(context.Background(), testRequest())
		requireClass(t, callErr, domain.ErrClassAuth)

		output := logs.String()
		if output == "" {
			t.Fatal("no log line was written, want one Debug line per call")
		}
		if strings.Contains(output, canaryKey) {
			t.Errorf("log output leaked the API key: %s", output)
		}
		if strings.Contains(callErr.Error(), canaryKey) {
			t.Errorf("error text leaked the API key: %v", callErr)
		}
		if !strings.Contains(output, `"outcome":"auth"`) {
			t.Errorf("log output is missing the auth outcome class: %s", output)
		}
	})
}
