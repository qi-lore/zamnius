package sqlite

import (
	"context"
	"fmt"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

// domainSchemaVersion and errMigrationRefused mirror the domain package. They are
// re-declared here so the store can describe its own failure without the caller
// having to translate, while errors.Is still matches the domain sentinel.
const domainSchemaVersion = domain.SchemaVersion

var errMigrationRefused = domain.ErrMigrationRefused

// migrations is the ordered persistence history. A migration is append-only:
// rewriting one that has shipped would invalidate every stored fingerprint.
var migrations = []struct {
	version    int
	statements []string
}{
	{
		version: 1,
		statements: []string{
			schemaV1,
		},
	},
}

// CheckRunVersion refuses state written by a schema this build cannot interpret
// (FR-017). A run whose recorded version is newer than the store's schema is a
// downgrade attempt; continuing would misinterpret fields silently.
func (s *Store) CheckRunVersion(ctx context.Context, run domain.Run) error {
	storeVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if run.SchemaVersion > storeVersion {
		return fmt.Errorf(
			"run %s was written with schema version %d but this build understands %d: %w",
			run.ID, run.SchemaVersion, storeVersion, domain.ErrMigrationRefused)
	}
	return nil
}
