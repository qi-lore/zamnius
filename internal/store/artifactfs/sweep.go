// Orphan artifact sweep. A file with no artifacts row is a valid consequence of
// the mandated write order (contracts/store.md §4): content is renamed into place
// and fsynced before its row is inserted in the committing transaction, so a crash
// between the two leaves an unreferenced file and never a row without content.
// Such a file is garbage only while nothing is executing, so the sweep refuses
// whenever a run is running rather than guess.
package artifactfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Catalog is the state the sweep needs: every content address a row references,
// and whether any run is still running. The SQLite store satisfies it
// (AllArtifactSHA256, RunningRuns), which keeps this package free of a dependency
// on the state store.
type Catalog interface {
	// AllArtifactSHA256 lists every digest that an artifacts row references.
	AllArtifactSHA256(ctx context.Context) (map[string]struct{}, error)
	// RunningRuns reports how many runs are currently marked running.
	RunningRuns(ctx context.Context) (int, error)
}

// SweepResult reports what a sweep did. Scanned counts well-formed content
// addresses considered; Reclaimed and Bytes describe the files removed.
type SweepResult struct {
	Scanned   int   `json:"scanned"`
	Reclaimed int   `json:"reclaimed"`
	Bytes     int64 `json:"bytes"`
}

// ErrWorkInFlight is returned when the sweep refuses because a run is running.
// Files are never deleted in that case, so a caller may retry once the run stops.
var ErrWorkInFlight = errors.New("artifact sweep refused: a run is running")

// digestHexLen is the length of a lowercase hex sha256, the shape of a content
// address and of nothing else the store writes into a shard directory.
const digestHexLen = 64

// Sweep deletes files under the artifact root that no artifacts row references.
//
// It refuses, deleting nothing, while any run is running: the only inconsistency
// the write order can produce is a rename that has not yet met its committing row,
// and that window exists only while a run is executing, so refusing then makes the
// race impossible rather than improbable. Only well-formed content addresses
// (<root>/<sha[0:2]>/<sha>, lowercase hex) are candidates; anything else is left
// untouched because the store never writes it there and does not own it.
func (s *Store) Sweep(ctx context.Context, catalog Catalog) (SweepResult, error) {
	if catalog == nil {
		return SweepResult{}, fmt.Errorf("sweep artifacts: catalog is required")
	}

	running, err := catalog.RunningRuns(ctx)
	if err != nil {
		return SweepResult{}, fmt.Errorf("sweep artifacts: %w", err)
	}
	if running > 0 {
		return SweepResult{}, fmt.Errorf("sweep artifacts: %w", ErrWorkInFlight)
	}

	known, err := catalog.AllArtifactSHA256(ctx)
	if err != nil {
		return SweepResult{}, fmt.Errorf("sweep artifacts: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SweepResult{}, fmt.Errorf("sweep artifacts: %w", err)
	}

	// Serialise against a concurrent sweep or a configuration change; Put itself
	// only reads this under RLock.
	s.mu.Lock()
	defer s.mu.Unlock()

	var result SweepResult
	shards, err := os.ReadDir(s.root)
	if err != nil {
		return result, fmt.Errorf("sweep artifacts: read %s: %w", s.root, err)
	}
	for _, shard := range shards {
		if !shard.IsDir() || !isShardDir(shard.Name()) {
			continue
		}
		shardDir := filepath.Join(s.root, shard.Name())
		files, err := os.ReadDir(shardDir)
		if err != nil {
			return result, fmt.Errorf("sweep artifacts: read %s: %w", shardDir, err)
		}
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("sweep artifacts: %w", err)
		}
		for _, file := range files {
			name := file.Name()
			// Candidates are exactly the paths the store writes: a 64-hex digest
			// whose first two characters name its own shard directory.
			if file.IsDir() || !isDigest(name) || shard.Name() != name[:2] {
				continue
			}
			result.Scanned++
			if _, ok := known[name]; ok {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return result, fmt.Errorf("sweep artifacts: stat %s: %w", name, err)
			}
			if err := os.Remove(filepath.Join(shardDir, name)); err != nil {
				return result, fmt.Errorf("sweep artifacts: remove %s: %w", name, err)
			}
			result.Reclaimed++
			result.Bytes += info.Size()
		}
	}
	return result, nil
}

func isShardDir(name string) bool {
	return len(name) == 2 && isLowerHex(name)
}

func isDigest(name string) bool {
	return len(name) == digestHexLen && isLowerHex(name)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
