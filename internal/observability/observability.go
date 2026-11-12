// Package observability wires structured logging and tracing without letting
// either leak into domain packages: slog is configured once at the composition
// root and OTel sits behind a thin interface whose default is a no-op, so library
// adopters are never forced into a global provider (research.md R8).
//
// Redaction lives here too. Secrets are read from the process environment and
// scrubbed from every log record before it is written; nothing else in the
// project is allowed to format a credential into an attribute (FR-054).
package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// RedactedValue replaces a recognised secret in any log record.
const RedactedValue = "[REDACTED]"

// SecretEnvVar is where the reference adapter's key lives. It is never logged,
// recorded in an event, or written into an artifact.
const SecretEnvVar = "ZAMNIUS_MODEL_API_KEY"

// SecretValues returns the secret values currently present in the environment.
// Empty values are skipped so a missing key cannot turn every string into a
// redaction target.
func SecretValues() []string {
	value := os.Getenv(SecretEnvVar)
	if value == "" {
		return nil
	}
	return []string{value}
}

// Redact replaces every occurrence of every secret in value.
func Redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, RedactedValue)
	}
	return value
}

// NewLogger builds a slog logger writing to w. format is "text" or "json"; level
// is "debug", "info", "warn", or "error". An invalid value is refused rather than
// defaulted, because a silently wrong log format breaks machine ingestion.
func NewLogger(w io.Writer, level, format string, secrets []string) (*slog.Logger, error) {
	parsedLevel, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	if w == nil {
		w = io.Discard
	}

	var base slog.Handler
	switch format {
	case "", "text":
		base = slog.NewTextHandler(w, &slog.HandlerOptions{Level: parsedLevel})
	case "json":
		base = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parsedLevel})
	default:
		return nil, fmt.Errorf("log format %q is not supported; accepted formats are text, json", format)
	}
	return slog.New(&redactingHandler{next: base, secrets: secrets}), nil
}

// ParseLevel maps the accepted level names onto slog levels.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log level %q is not supported; accepted levels are debug, info, warn, error", level)
	}
}

// redactingHandler scrubs secrets from messages and string attributes, including
// attributes attached with With and groups opened with WithGroup.
type redactingHandler struct {
	next    slog.Handler
	secrets []string
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	redacted := slog.NewRecord(record.Time, record.Level, Redact(record.Message, h.secrets), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		redacted.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, redacted)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, h.redactAttr(attr))
	}
	return &redactingHandler{next: h.next.WithAttrs(redacted), secrets: h.secrets}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), secrets: h.secrets}
}

func (h *redactingHandler) redactAttr(attr slog.Attr) slog.Attr {
	value := attr.Value
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, Redact(value.String(), h.secrets))
	case slog.KindGroup:
		group := value.Group()
		redacted := make([]slog.Attr, 0, len(group))
		for _, nested := range group {
			redacted = append(redacted, h.redactAttr(nested))
		}
		return slog.Group(attr.Key, attrsToAny(redacted)...)
	case slog.KindAny:
		if text, ok := value.Any().(string); ok {
			return slog.String(attr.Key, Redact(text, h.secrets))
		}
		if err, ok := value.Any().(error); ok {
			return slog.String(attr.Key, Redact(err.Error(), h.secrets))
		}
		return attr
	default:
		return attr
	}
}

func attrsToAny(attrs []slog.Attr) []any {
	out := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, attr)
	}
	return out
}

// Tracer is the tracing seam. The default implementation does nothing, so a
// library adopter who never configures OTel pays nothing and shares no global
// state with another process.
type Tracer interface {
	Start(ctx context.Context, name string, attrs map[string]string) (context.Context, Span)
}

// Span is the minimal span surface the engine needs.
type Span interface {
	End()
}

// NoopTracer returns a tracer that discards every span.
func NoopTracer() Tracer { return noopTracer{} }

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ map[string]string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End() {}
