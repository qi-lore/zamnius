package artifactfs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hIMEI29A/zamnius/internal/domain"
	"github.com/hIMEI29A/zamnius/internal/fault"
	"github.com/hIMEI29A/zamnius/internal/store/artifactfs"
)

func TestPutWritesVerifiableContentAddressedFile(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	content := "# plan\n\n1. do the thing\n"

	ref, err := store.Put(context.Background(), uuid.New(), "plan", "text/markdown", strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if want := domain.HashBytes([]byte(content)); ref.SHA256 != want {
		t.Errorf("sha256 = %q, want %q", ref.SHA256, want)
	}
	if ref.SizeBytes != int64(len(content)) {
		t.Errorf("size = %d, want %d", ref.SizeBytes, len(content))
	}
	if ref.Kind != "plan" || ref.MediaType != "text/markdown" {
		t.Errorf("descriptor lost kind/media type: %+v", ref)
	}
	// The recorded path is relative to the state directory, not the process cwd.
	wantRel := filepath.Join("artifacts", ref.SHA256[:2], ref.SHA256)
	if ref.RelativePath != wantRel {
		t.Errorf("relative path = %q, want %q", ref.RelativePath, wantRel)
	}
	if digest := domain.HashBytes([]byte(content)); ref.SHA256 != digest {
		t.Errorf("content digest mismatch")
	}

	reader, err := store.Get(context.Background(), ref.SHA256)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestPutIsIdempotentByContent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	content := "same bytes\n"

	first, err := store.Put(context.Background(), uuid.New(), "report", "", strings.NewReader(content))
	if err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	second, err := store.Put(context.Background(), uuid.New(), "report", "", strings.NewReader(content))
	if err != nil {
		t.Fatalf("second Put() error = %v", err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatal("identical content produced different digests")
	}

	// One file, not two: retrying an attempt must not duplicate side effects.
	entries, err := os.ReadDir(filepath.Join(store.Root(), first.SHA256[:2]))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("shard holds %d files, want 1", len(entries))
	}
	// Default media type keeps the audit record complete.
	if second.MediaType != "text/markdown" {
		t.Errorf("default media type = %q, want text/markdown", second.MediaType)
	}
}

func TestGetRejectsTamperedContent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	ref, err := store.Put(context.Background(), uuid.New(), "report", "", strings.NewReader("original\n"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if err := os.WriteFile(store.Path(ref.SHA256), []byte("tampered\n"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if _, err := store.Get(context.Background(), ref.SHA256); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("Get() error = %v, want ErrIntegrity", err)
	}
	if err := store.Verify(context.Background(), ref.SHA256); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("Verify() error = %v, want ErrIntegrity", err)
	}
}

func TestVerifyMissingFileIsNotFound(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	missing := strings.Repeat("a", 64)
	if err := store.Verify(context.Background(), missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Verify() error = %v, want ErrNotFound", err)
	}
}

// TestFaultPointBeforeRenameLeavesNoCommittedArtifact must not run in parallel:
// fault arming is process-global, so a concurrent Put in another test would be
// interrupted by mistake.
func TestFaultPointBeforeRenameLeavesNoCommittedArtifact(t *testing.T) {
	root := t.TempDir()
	store, err := artifactfs.Open(root)
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}

	fault.Arm(fault.BeforeArtifactRename)
	func() {
		defer func() {
			recovered := recover()
			if _, ok := fault.AsTriggered(recovered); !ok {
				t.Fatalf("expected an injected fault, recovered %v", recovered)
			}
		}()
		_, _ = store.Put(context.Background(), uuid.New(), "plan", "", strings.NewReader("content\n"))
	}()
	fault.Disarm()

	// No content-addressed file may exist: the rename never happened, so nothing
	// can be referenced by a row.
	content := []byte("content\n")
	digest := domain.HashBytes(content)
	if _, err := os.Stat(store.Path(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a final artifact file survived a pre-rename interruption: %v", err)
	}

	// The interrupted write left a temp file behind, which is the recoverable
	// state by design; reopening sweeps it.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	sawTemp := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "zamnius-artifact-") {
			sawTemp = true
		}
	}
	if !sawTemp {
		t.Fatal("expected the interrupted write to leave a temp file for the sweep")
	}
	if _, err := artifactfs.Open(root); err != nil {
		t.Fatalf("reopen artifact store: %v", err)
	}
	entries, err = os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root after sweep: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "zamnius-artifact-") {
			t.Fatalf("temp file %q survived the sweep", entry.Name())
		}
	}
}

func TestOpenSweepsStaleTemps(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stale := filepath.Join(root, "zamnius-artifact-123456")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write stale temp: %v", err)
	}
	if _, err := artifactfs.Open(root); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp survived Open: %v", err)
	}
}

func newStore(t *testing.T) *artifactfs.Store {
	t.Helper()
	store, err := artifactfs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}
	return store
}
