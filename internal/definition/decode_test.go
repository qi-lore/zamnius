package definition_test

import (
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/definition"
	"github.com/qi-lore/zamnius/internal/domain"
)

// onePhaseJSON is the smallest definition that decodes; each test mutates one key.
const onePhaseJSON = `{
  "version": "1",
  "name": "decode",
  "phases": [{"name": "plan", "mode": "single", "agent": {"name": "planner", "instructions": "make a plan", "model": {"provider": "openai-compatible", "name": "gpt-4.1-mini"}}}]
}`

func TestDecodeRefusesADuplicateJSONKey(t *testing.T) {
	t.Parallel()

	// Both surfaces decode through internal/strict, so a document that is refused
	// as a process-settings file cannot be silently accepted as a definition
	// (ADR-0010). Before the shared decoder this decoded as name = "second".
	doc := strings.Replace(onePhaseJSON, `"name": "decode"`, `"name": "decode", "name": "second"`, 1)

	_, err := definition.Decode([]byte(doc), definition.FormatJSON)
	if err == nil {
		t.Fatalf("Decode() accepted a duplicate key")
	}
	if !strings.Contains(domain.HintOf(err), "duplicate key") {
		t.Errorf("hint = %q, want it to name the duplicate key", domain.HintOf(err))
	}
	if got := domain.ClassOf(err); got != domain.ErrClassPermanent {
		t.Errorf("class = %q, want permanent", got)
	}
}
