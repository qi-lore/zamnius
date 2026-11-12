package observability_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/hIMEI29A/zamnius/internal/observability"
)

func TestLoggerRedactsSecretsFromMessageAndAttributes(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := observability.NewLogger(&buffer, "debug", "json", []string{"sk-test-canary"})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	logger.Info("calling provider with sk-test-canary",
		"authorization", "Bearer sk-test-canary",
		"model", "gpt-4.1-mini",
		slog.Group("request", "api_key", "sk-test-canary"),
	)

	output := buffer.String()
	if strings.Contains(output, "sk-test-canary") {
		t.Fatalf("secret leaked into the log:\n%s", output)
	}
	if !strings.Contains(output, observability.RedactedValue) {
		t.Fatalf("redaction marker missing:\n%s", output)
	}
	if !strings.Contains(output, "gpt-4.1-mini") {
		t.Fatalf("non-secret fields must survive:\n%s", output)
	}
}

func TestLoggerRedactsAttributesAttachedWithWith(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := observability.NewLogger(&buffer, "info", "text", []string{"sk-test-canary"})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	logger.With("api_key", "sk-test-canary").Info("attempt started")
	if strings.Contains(buffer.String(), "sk-test-canary") {
		t.Fatalf("secret leaked through With:\n%s", buffer.String())
	}
}

func TestLoggerRedactsErrors(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := observability.NewLogger(&buffer, "error", "json", []string{"sk-test-canary"})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	logger.Error("provider failed", "err", errString("401 for key sk-test-canary"))
	if strings.Contains(buffer.String(), "sk-test-canary") {
		t.Fatalf("secret leaked through an error attribute:\n%s", buffer.String())
	}
}

func TestLoggerRejectsUnknownFormatAndLevel(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	if _, err := observability.NewLogger(&buffer, "info", "yaml", nil); err == nil {
		t.Fatal("accepted an unsupported log format")
	}
	if _, err := observability.NewLogger(&buffer, "verbose", "text", nil); err == nil {
		t.Fatal("accepted an unsupported log level")
	}
}

func TestNoopTracerReturnsSpan(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	tracer := observability.NoopTracer()
	next, span := tracer.Start(ctx, "run", map[string]string{"run_id": "1"})
	if next == nil {
		t.Fatal("tracer dropped the context")
	}
	span.End() // must not panic
}

type errString string

func (e errString) Error() string { return string(e) }
