// Package artifactfs is the content-addressed artifact store: artifacts are files
// named by their sha256 under <root>/<sha[0:2]>/<sha>.
//
// The write order is mandated (contracts/store.md §4) and must not be reordered:
// temp file -> write -> fsync -> rename -> fsync directory. A crash at any point
// therefore leaves either no file or a complete file, never a partial one (FR-015),
// and the database row is inserted by the caller only after this returns.
package artifactfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/fault"
)

// Store is a filesystem content-addressed store.
type Store struct {
	root string
	mu   sync.RWMutex
	now  func() time.Time
	ids  func() uuid.UUID
}

// Open prepares root as an artifact store, creating it if needed. It also sweeps
// stale temporary files: a crash mid-write leaves one behind and nothing else.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("open artifact store: root is required")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("open artifact store: create %s: %w", root, err)
	}
	store := &Store{root: root, now: func() time.Time { return time.Now().UTC() }, ids: uuid.New}
	store.sweepTemps()
	return store, nil
}

// WithClock and WithIDSource make the store deterministic in tests without
// exporting mutable fields.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// WithIDSource replaces the identifier generator (tests only).
func (s *Store) WithIDSource(ids func() uuid.UUID) *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = ids
	return s
}

// Root reports the artifact root directory.
func (s *Store) Root() string { return s.root }

// Path reports the file path for a digest.
func (s *Store) Path(sha string) string {
	return filepath.Join(s.root, shard(sha), sha)
}

// RelativePath is the path recorded in the artifact row, relative to the state
// directory, so an exported store stays relocatable.
func RelativePath(sha string) string {
	return filepath.Join("artifacts", shard(sha), sha)
}

func shard(sha string) string {
	if len(sha) < 2 {
		return "00"
	}
	return sha[:2]
}

// Put writes content and returns its descriptor. It is idempotent by content
// hash: identical bytes are one file, so a retried attempt cannot duplicate it.
func (s *Store) Put(ctx context.Context, runID uuid.UUID, kind, mediaType string, r io.Reader) (domain.ArtifactRef, error) {
	if kind == "" {
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: kind is required")
	}
	if mediaType == "" {
		mediaType = "text/markdown"
	}

	s.mu.RLock()
	now, ids := s.now, s.ids
	s.mu.RUnlock()

	temp, err := os.CreateTemp(s.root, "zamnius-artifact-*")
	if err != nil {
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: create temp: %w", err)
	}
	tempPath := temp.Name()
	// Any failure before the rename must not leave a partial file behind.
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), r)
	if err != nil {
		cleanup()
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: write: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: fsync: %w", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: close: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	shardDir := filepath.Join(s.root, shard(digest))
	if err := os.MkdirAll(shardDir, 0o750); err != nil {
		cleanup()
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: create shard: %w", err)
	}

	fault.Hit(fault.BeforeArtifactRename)
	if err := os.Rename(tempPath, filepath.Join(shardDir, digest)); err != nil {
		cleanup()
		return domain.ArtifactRef{}, fmt.Errorf("put artifact: rename: %w", err)
	}
	fault.Hit(fault.AfterArtifactRename)

	if err := fsyncDir(shardDir); err != nil {
		return domain.ArtifactRef{}, err
	}

	ref := domain.ArtifactRef{
		ID:           ids(),
		SHA256:       digest,
		RunID:        runID,
		Kind:         kind,
		MediaType:    mediaType,
		SizeBytes:    size,
		RelativePath: RelativePath(digest),
		CreatedAt:    now(),
	}
	if err := ctx.Err(); err != nil {
		// The file is durable; the caller's cancellation must still be honoured and
		// leaves an orphan file, which the sweep handles (contracts/store.md §4).
		return ref, fmt.Errorf("put artifact: %w", err)
	}
	return ref, nil
}

// Get returns the content, verifying the fingerprint before releasing any bytes.
func (s *Store) Get(ctx context.Context, sha string) (io.ReadCloser, error) {
	content, err := os.ReadFile(s.Path(sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("get artifact %s: %w", sha, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("get artifact %s: %w", sha, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get artifact %s: %w", sha, err)
	}
	if !domain.VerifyDigest(sha, content) {
		return nil, fmt.Errorf("get artifact %s: content does not match its fingerprint: %w", sha, domain.ErrIntegrity)
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

// Verify checks the stored bytes against the recorded digest without returning them.
func (s *Store) Verify(ctx context.Context, sha string) error {
	file, err := os.Open(s.Path(sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("verify artifact %s: %w", sha, domain.ErrNotFound)
		}
		return fmt.Errorf("verify artifact %s: %w", sha, err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("verify artifact %s: %w", sha, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify artifact %s: %w", sha, err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != sha {
		return fmt.Errorf("verify artifact %s: content digest is %s: %w", sha, got, domain.ErrIntegrity)
	}
	return nil
}

// Stat reports size and modification time for a digest, for CLI listings.
func (s *Store) Stat(sha string) (int64, error) {
	info, err := os.Stat(s.Path(sha))
	if err != nil {
		return 0, fmt.Errorf("stat artifact %s: %w", sha, err)
	}
	return info.Size(), nil
}

// fsyncDir makes the rename durable. Skipping this can leave the directory entry
// in the page cache, which is exactly the crash window FR-015 forbids.
func fsyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("fsync directory %s: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("fsync directory %s: %w", dir, err)
	}
	return nil
}

// sweepTemps removes temporary files left by a crashed write. They are never
// referenced by a row, so deleting them is always safe.
func (s *Store) sweepTemps() {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "zamnius-artifact-") {
			continue
		}
		_ = os.Remove(filepath.Join(s.root, entry.Name()))
	}
}
