package observability_test

import (
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/observability"
)

func TestRedactSkipsEmptySecrets(t *testing.T) {
	t.Parallel()

	if got := observability.Redact("unchanged", []string{""}); got != "unchanged" {
		t.Errorf("Redact with an empty secret = %q", got)
	}
	if got := observability.Redact("has secret inside", []string{"secret"}); strings.Contains(got, "secret") {
		t.Errorf("Redact() = %q", got)
	}
}

func TestRedactHandlesOverlappingSecrets(t *testing.T) {
	t.Parallel()

	// The longer secret must win, otherwise a prefix match leaks the remainder.
	got := observability.Redact("key=sk-test-canary-extended", []string{"sk-test-canary", "sk-test-canary-extended"})
	if strings.Contains(got, "sk-test-canary") {
		t.Fatalf("Redact() = %q, want the full secret removed", got)
	}
}

func TestSecretValuesReadsOnlyTheDocumentedVariable(t *testing.T) {
	// Not parallel: t.Setenv mutates the process environment.
	t.Setenv(observability.SecretEnvVar, "sk-test-canary")
	secrets := observability.SecretValues()
	if len(secrets) != 1 || secrets[0] != "sk-test-canary" {
		t.Fatalf("SecretValues() = %v", secrets)
	}

	t.Setenv(observability.SecretEnvVar, "")
	if got := observability.SecretValues(); len(got) != 0 {
		t.Fatalf("SecretValues() = %v, want none for an unset key", got)
	}
}
