package zamnius

import "github.com/hIMEI29A/zamnius/internal/domain"

// Exported error vocabulary. Callers match with errors.Is, e.g.
//
//	if errors.Is(err, zamnius.ErrRunAlreadyOwned) { ... }
//
// Every returned error carries a domain.ErrorClass, so retry and exit-code
// decisions never parse message strings.
var (
	// ErrRunAlreadyOwned means another live process holds the run's lease.
	ErrRunAlreadyOwned = domain.ErrRunAlreadyOwned
	// ErrStaleRecord means a writer held an outdated record version.
	ErrStaleRecord = domain.ErrStaleRecord
	// ErrIntegrity means persisted state is inconsistent with itself.
	ErrIntegrity = domain.ErrIntegrity
	// ErrInvalidDefinition means a workflow definition failed validation.
	ErrInvalidDefinition = domain.ErrInvalidDefinition
	// ErrInvalidConfig means engine configuration failed validation.
	ErrInvalidConfig = domain.ErrInvalidConfig
	// ErrTerminalRun means the run already reached a terminal status.
	ErrTerminalRun = domain.ErrTerminalRun
	// ErrDefinitionDrift means the definition no longer matches the run snapshot.
	ErrDefinitionDrift = domain.ErrDefinitionDrift
	// ErrMigrationRefused means persisted state cannot be interpreted by this build.
	ErrMigrationRefused = domain.ErrMigrationRefused
)

// ClassOf returns the classification carried by err, defaulting to internal.
func ClassOf(err error) ErrorClass { return domain.ClassOf(err) }

// HintOf returns the operator-facing hint carried by err, if any. Configuration
// and integrity failures always carry one, naming the setting or file to fix.
func HintOf(err error) string { return domain.HintOf(err) }
