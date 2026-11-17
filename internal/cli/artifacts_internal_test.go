package cli

// This file covers T061 at the level where the export is deterministic: the copy
// loop and its verification are exercised against a fake read path, so a digest
// mismatch can be asserted without running a workflow or corrupting a real store
// file. The end-to-end path through a real run — a corrupted artifact file making
// the command exit `6` — is T062's integration test (contracts/cli.md §1, §3).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/pkg/zamnius"
)

// fakeArtifactContent is the export's read path without a content-addressed store.
// It returns the configured content by digest and can fail the way the store does:
// a wrapping of domain.ErrIntegrity for a mismatch, or domain.ErrNotFound for a
// file that is gone.
type fakeArtifactContent struct {
	content map[string]string
	err     error
}

func (f *fakeArtifactContent) Get(_ context.Context, sha string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, ok := f.content[sha]
	if !ok {
		return nil, fmt.Errorf("get artifact %s: %w", sha, domain.ErrNotFound)
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (f *fakeArtifactContent) Path(sha string) string {
	return filepath.Join("state", "artifacts", sha[:2], sha)
}

func TestExportArtifactsWritesExactlyTheVerifiedContent(t *testing.T) {
	t.Parallel()

	body := "the verified body\n"
	sha := domain.HashBytes([]byte(body))
	store := &fakeArtifactContent{content: map[string]string{sha: body}}
	r := &runner{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard}
	dir := t.TempDir()

	if err := r.exportArtifacts(store, []zamnius.ArtifactRef{{SHA256: sha, Kind: "plan"}}, dir); err != nil {
		t.Fatalf("exportArtifacts() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "plan-"+sha))
	if err != nil {
		t.Fatalf("exported file: %v", err)
	}
	if string(got) != body {
		t.Errorf("exported content = %q, want %q", got, body)
	}
}

func TestExportArtifactsAbortsOnFingerprintMismatch(t *testing.T) {
	t.Parallel()

	sha := domain.HashBytes([]byte("the body"))
	store := &fakeArtifactContent{err: fmt.Errorf("get artifact %s: content does not match its fingerprint: %w", sha, domain.ErrIntegrity)}
	r := &runner{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard}
	dir := t.TempDir()

	err := r.exportArtifacts(store, []zamnius.ArtifactRef{{SHA256: sha, Kind: "plan"}}, dir)
	if !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("exportArtifacts() error = %v, want it to wrap domain.ErrIntegrity", err)
	}
	if code := exitCodeFor(err, ""); code != ExitIntegrity {
		t.Errorf("exit code = %d, want %d", code, ExitIntegrity)
	}
	// The diagnostic must name the stored file and the digest it should have had.
	if !strings.Contains(err.Error(), sha) {
		t.Errorf("message %q does not name the digest", err.Error())
	}
	if hint := domain.HintOf(err); !strings.Contains(hint, sha) || !strings.Contains(hint, sha[:2]) {
		t.Errorf("hint %q does not name the digest and its file", hint)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("a mismatched artifact was written: %v", entries)
	}
}

func TestExportArtifactsTreatsAMissingFileAsIntegrity(t *testing.T) {
	t.Parallel()

	sha := domain.HashBytes([]byte("gone"))
	store := &fakeArtifactContent{err: fmt.Errorf("get artifact %s: %w", sha, domain.ErrNotFound)}
	r := &runner{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard}

	err := r.exportArtifacts(store, []zamnius.ArtifactRef{{SHA256: sha, Kind: "plan"}}, t.TempDir())
	if !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("exportArtifacts() error = %v, want it to wrap domain.ErrIntegrity", err)
	}
	if code := exitCodeFor(err, ""); code != ExitIntegrity {
		t.Errorf("exit code = %d, want %d", code, ExitIntegrity)
	}
}
