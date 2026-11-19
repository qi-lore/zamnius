// Redaction helpers. They live apart from the handler so the engine can scrub a
// value before it reaches a log, an event payload, or an artifact without
// constructing a logger at all (FR-054).
package observability

import (
	"os"
	"strings"

	"github.com/qi-lore/zamnius/internal/domain"
)

// RedactedValue replaces a recognised secret in any log record.
const RedactedValue = "[REDACTED]"

// SecretEnvVar is where the reference adapter's key lives. It is never logged,
// recorded in an event, or written into an artifact.
const SecretEnvVar = domain.ModelAPIKeyEnvVar

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
