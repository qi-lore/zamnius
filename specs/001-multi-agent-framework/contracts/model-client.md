# Contract: provider-neutral model access

Public surface: `pkg/zamnius.ModelClient` (implemented in `internal/model`).
Reference adapter: `internal/model/openai` — OpenAI-compatible Chat Completions.
Related: [research.md R9](../research.md), ADR-0006.

This contract is the **only** way the core reaches a model. No package outside `internal/model/openai`
may contain provider-specific request or response shapes.

---

## 1. Interface shape

```go
// ModelClient completes one model interaction.
type ModelClient interface {
    // Complete blocks until the model answers, the context ends, or an error occurs.
    // A returned error MUST be classified (see §4). Usage MUST be populated.
    Complete(ctx context.Context, req Request) (Response, error)
}

type Request struct {
    Model           string            // provider-specific model name, e.g. "gpt-4.1-mini"
    System          string            // system instructions
    Messages        []Message         // conversation so far, oldest first
    MaxOutputTokens int               // > 0; required
    Temperature     *float64          // nil = provider default
    ResponseSchema  json.RawMessage   // optional; JSON Schema the answer must satisfy
    Metadata        map[string]string // correlation only: run_id, phase, attempt_id
}

type Message struct {
    Role    Role   // user | assistant
    Content string
}

type Response struct {
    Content     string
    Usage       Usage
    StopReason  StopReason // stop | length | content_filter | unknown
}

type Usage struct {
    InputTokens  int
    OutputTokens int
}
```

**Why `Messages` has only `user` and `assistant`**: tool calling and system-role messages per provider are out of scope for this increment; the system prompt is a dedicated field so adapters can map it correctly per provider.

---

## 2. Required behaviour of any implementation

1. **Cancellation** — must return promptly when `ctx` is done, classified as `cancelled`. Partial usage is reported when the provider exposed it.
2. **Usage reporting** — must report input and output tokens. If the provider omits usage, the call fails with `invalid_response`; it is never estimated, because budgets (FR-007) depend on it.
3. **Schema conformance** — when `ResponseSchema` is set, the returned `Content` must be valid JSON conforming to it. Adapters must request structured output where the provider supports it and **always** re-validate locally.
4. **Error classification** — every error is classified into exactly one `ErrorClass` (§4). Unclassified transport errors are `internal`.
5. **No persisted provider state** — no continuation tokens, thread IDs, or cache keys are recorded in run state; a resumed run may use a different provider (ADR-0006 consequence).
6. **Determinism for tests** — implementation must be replaceable by a fake in every test path; no package may construct a concrete adapter outside the composition root (`internal/app`).

---

## 3. Reference adapter: OpenAI-compatible

| Concern | Behaviour |
|---|---|
| Endpoint | `POST {base_url}/chat/completions`, default `https://api.openai.com/v1` |
| Auth | `Authorization: Bearer $ZAMNIUS_MODEL_API_KEY`; the key is read from the environment, never from a workflow definition |
| Request mapping | `system` + `messages` → `messages[]`; `max_output_tokens` → `max_tokens`; `temperature` passthrough |
| Structured output | `response_format: {type: "json_schema", json_schema: {name, schema, strict: true}}`; if the server rejects it (`400` naming `response_format`), retry once in `json_object` mode and validate locally |
| Usage | `usage.prompt_tokens` / `usage.completion_tokens` |
| Stop reason | `finish_reason` → `stop` / `length` / `content_filter`; anything else → `unknown` |
| Timeouts | No internal timeout; the caller's `context` governs. Adapters must not add hidden retries — retry policy belongs to the engine |
| Streaming | Not implemented in this increment |

**Redaction**: the API key and any `Authorization` header value are never logged, recorded in events, or written into artifacts. Request/response logging records model name, token counts, and duration only.

---

## 4. Error classification

| `ErrorClass` | Trigger examples | Default retry eligible |
|---|---|---|
| `transient` | connection reset, 502/503/504, DNS blip | yes |
| `rate_limited` | 429 | yes (honours `Retry-After` if present) |
| `auth` | 401/403 | no |
| `permanent` | 400/404/422 that are not schema-related | no |
| `context_overflow` | provider reports context length exceeded | no |
| `invalid_response` | body unparsable, usage missing, schema non-conformance | no |
| `timeout` | caller context deadline exceeded | yes |
| `budget_exhausted` | engine-side stop before calling the model | no |
| `cancelled` | caller cancellation | no |
| `internal` | anything unclassified | no |

The engine maps `ErrorClass` against `phase.retry.retry_on` to decide whether another attempt is allowed; adapters never retry on their own.

---

## 5. Budget interaction

The engine checks remaining allowance **before** a call (attempts, wall-clock) and **after** it (tokens). A response whose token usage alone exceeds the remaining phase budget is recorded (usage is real) but the next attempt is not started: the phase ends with `budget_exhausted` naming `tokens`.

---

## 6. Conformance test expectations

Any adapter must pass a shared suite against an `httptest` server:

1. Successful completion populates content, usage, and stop reason.
2. 429 → `rate_limited`, 401 → `auth`, 500 → `transient`, malformed body → `invalid_response`.
3. Missing `usage` → `invalid_response`, not a zero-value success.
4. Schema rejection fallback path returns validated JSON or `invalid_response`.
5. Context cancellation returns `cancelled` within the cancellation deadline plus a small grace period.
6. No secret appears in any log line captured during the suite.
