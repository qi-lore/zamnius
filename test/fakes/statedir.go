package fakes

import (
	"os"
	"path/filepath"
	"testing"
)

// TempStateDir builds an isolated state directory for a test: <tmp>/state with
// the artifacts subdirectory the store expects. It is a t.Helper so a failure
// inside it points at the caller, and it registers cleanup so no test leaves
// scratch state behind (test/.state is the manual-run equivalent).
func TempStateDir(t testing.TB) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o750); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	// The state directory must be writable by the owner only: it will hold the
	// audit record, and a world-readable trail is a disclosure risk (FR-054).
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("restrict state directory permissions: %v", err)
	}
	return dir
}

// StatePaths reports the paths a caller will need under a state directory, so a
// test does not repeat the layout.
type StatePaths struct {
	Dir      string
	Database string
	Artifact string
}

// StateLayout returns the standard paths of a state directory.
func StateLayout(dir string) StatePaths {
	return StatePaths{
		Dir:      dir,
		Database: filepath.Join(dir, "zamnius.db"),
		Artifact: filepath.Join(dir, "artifacts"),
	}
}
