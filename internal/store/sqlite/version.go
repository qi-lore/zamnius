package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
)

// domainSchemaVersion and errMigrationRefused mirror the domain package. They are
// re-declared here so the store can describe its own failure without the caller
// having to translate, while errors.Is still matches the domain sentinel.
const domainSchemaVersion = domain.SchemaVersion

var errMigrationRefused = domain.ErrMigrationRefused

// migration is one append-only step of the persistence history. Rewriting a step
// that has shipped would invalidate every stored row written after it.
type migration struct {
	version    int
	statements []string
}

// migrations is the ordered persistence history.
var migrations = []migration{
	{
		version: 1,
		statements: []string{
			schemaV1,
		},
	},
}

// SchemaVersion reports the highest applied migration. It is the version of the
// state on disk, which is what a run's recorded schema_version is compared with.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// CheckRunVersion reports how a run's recorded schema version compares with this
// store's, and refuses state written by a newer build (FR-017). A run recorded
// under an older schema is not an error: the store has already applied the pending
// migrations at open, and the caller records that fact on the run. A newer schema is
// a downgrade: continuing would misinterpret fields silently, so it is refused with
// a hint naming the build the operator needs.
func (s *Store) CheckRunVersion(ctx context.Context, run domain.Run) (domain.RunSchemaStatus, error) {
	storeVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		return domain.RunSchemaStatus{}, err
	}
	status := domain.RunSchemaStatus{RunVersion: run.SchemaVersion, StoreVersion: storeVersion}
	if status.Newer() {
		return status, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg: fmt.Sprintf("run %s was written with schema version %d but this build understands %d",
				run.ID, status.RunVersion, status.StoreVersion),
			Hint: fmt.Sprintf("upgrade zamnius to a build that understands schema version %d before resuming run %s; this build understands only schema version %d and refuses to interpret a newer record instead of guessing",
				status.RunVersion, run.ID, status.StoreVersion),
			Err: domain.ErrMigrationRefused,
		}
	}
	return status, nil
}

// shippedSchemaVersion is the newest migration this build ships, which is the
// schema it can interpret. It equals domain.SchemaVersion — asserted in
// version_internal_test.go, so the constant that names the format and the list that
// gets there cannot drift apart. Reading it from the list rather than the constant is
// what lets the migration machinery be tested with a migration this build does not
// ship.
func shippedSchemaVersion() int {
	newest := 0
	for _, step := range migrations {
		if step.version > newest {
			newest = step.version
		}
	}
	return newest
}

// migrate applies every migration newer than the recorded version. Each migration
// is applied inside one transaction so a crash cannot record a version whose
// statements did not run.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaVersionTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if newest := shippedSchemaVersion(); current > newest {
		return &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg: fmt.Sprintf("store schema version %d is newer than this build (%d)",
				current, newest),
			Hint: fmt.Sprintf("upgrade zamnius to a build that understands schema version %d before opening this state directory; this build refuses to interpret a newer format",
				current),
			Err: errMigrationRefused,
		}
	}

	for _, step := range migrations {
		if step.version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", step.version, err)
		}
		for _, stmt := range step.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply migration %d: %w", step.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			step.version, formatTime(time.Now()),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", step.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", step.version, err)
		}
	}
	return nil
}
