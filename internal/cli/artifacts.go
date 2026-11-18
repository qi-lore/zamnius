package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/pkg/zamnius"
)

// artifactContent is the read path the export needs: fetch content by digest, and
// locate the stored file for a diagnostic. The content-addressed store satisfies it,
// and it is an interface so the export loop is testable without a real run.
// Content is fetched through the artifact store, never inlined by the engine
// (contracts/library-api.md §2).
type artifactContent interface {
	Get(ctx context.Context, sha string) (io.ReadCloser, error)
	Path(sha string) string
}

// runArtifacts executes `zamnius run artifacts`: list the committed artifacts, and
// with --out copy them to a directory, verifying each fingerprint before its bytes
// are written. A mismatch aborts with exit `6` (contracts/cli.md §1).
func (r *runner) runArtifacts(args []string) int {
	const command = "run.artifacts"

	positional, flags := splitArgs(args)
	if len(positional) != 1 {
		return r.usage(command + " needs exactly one run id")
	}

	var out string
	var shared runFlags
	set := r.newFlagSet(command)
	addCommon(set, &shared)
	set.StringVar(&out, "out", "", "directory to export the run's artifacts into")
	if ok, code := r.parseFlags(set, flags); !ok {
		return code
	}
	r.json = shared.json

	runID, parseErr := uuid.Parse(positional[0])
	if parseErr != nil {
		return r.usage(fmt.Sprintf("%q is not a run id", positional[0]))
	}

	composition, closeFn, err := r.openSettings(shared)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	defer closeFn()

	// Listing and exporting read the record only, so the inspection engine is used:
	// an artifact stays exportable after the key that produced it has expired.
	engine, err := composition.Inspector()
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}
	refs, err := engine.Artifacts(r.ctx, runID)
	if err != nil {
		return r.finish(command, nil, nil, "", err, nil)
	}

	if out == "" {
		return r.finish(command, nil, artifactViews(refs), "", nil, func() { r.humanArtifacts(refs, runID) })
	}
	if err := r.exportArtifacts(composition.Artifacts, refs, out); err != nil {
		return r.finish(command, nil, artifactViews(refs), "", err, nil)
	}
	return r.finish(command, nil, artifactViews(refs), "", nil, func() { r.humanExport(refs, out) })
}

// exportArtifacts copies each committed artifact into dir. The content-addressed
// store verifies a file's fingerprint before releasing its bytes, so the digest is
// confirmed before anything is written: a mismatch aborts and leaves no corrupt
// copy behind. A missing file is the same disagreement between the record and its
// content, so it fails the same way (contracts/cli.md §1, §3; domain.ErrIntegrity).
func (r *runner) exportArtifacts(store artifactContent, refs []zamnius.ArtifactRef, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("export artifacts: create %s: %w", dir, err)
	}
	for _, ref := range refs {
		content, err := store.Get(r.ctx, ref.SHA256)
		if err != nil {
			return artifactIntegrityFailure(store, ref, err)
		}
		if err := writeArtifact(filepath.Join(dir, exportName(ref)), content); err != nil {
			return err
		}
	}
	return nil
}

// exportName is the exported file's name: the artifact's kind and digest, which
// together are unique within a run (a digest is one content, so one file).
func exportName(ref zamnius.ArtifactRef) string {
	return ref.Kind + "-" + ref.SHA256
}

// writeArtifact streams content to dest and closes it. The bytes were verified by
// the store before this call, so a failure here is an I/O failure, not an integrity
// one.
func writeArtifact(dest string, content io.ReadCloser) error {
	defer func() { _ = content.Close() }()

	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("export artifacts: create %s: %w", dest, err)
	}
	if _, err := io.Copy(file, content); err != nil {
		_ = file.Close()
		return fmt.Errorf("export artifacts: write %s: %w", dest, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("export artifacts: write %s: %w", dest, err)
	}
	return nil
}

// artifactIntegrityFailure classifies any failed read as the persistence integrity
// failure the operator must act on: the stored record and the content it points at
// disagree. The hint names the stored file and the digest it should have had, which
// is what lets an operator find and repair it (contracts/cli.md §2, §3).
func artifactIntegrityFailure(store artifactContent, ref zamnius.ArtifactRef, cause error) error {
	return &domain.Error{
		Class: domain.ErrClassInternal,
		Msg:   fmt.Sprintf("artifact %s (%s) cannot be exported", ref.SHA256, ref.Kind),
		Hint: fmt.Sprintf("expected digest %s in %s; the stored file is missing or its content does not match",
			ref.SHA256, store.Path(ref.SHA256)),
		Err: errors.Join(domain.ErrIntegrity, cause),
	}
}

// humanArtifacts prints the artifact listing: one line per artifact, then a count.
func (r *runner) humanArtifacts(refs []zamnius.ArtifactRef, runID uuid.UUID) {
	for _, artifact := range refs {
		writeLine(r.stdout, "%s %s %d bytes %s",
			artifact.Kind, artifact.SHA256, artifact.SizeBytes, artifact.Phase)
	}
	writeLine(r.stdout, "%s of run %s", plural(len(refs), "artifact"), runID)
}

// humanExport prints where each artifact was written, then a count.
func (r *runner) humanExport(refs []zamnius.ArtifactRef, dir string) {
	for _, artifact := range refs {
		writeLine(r.stdout, "wrote %s", filepath.Join(dir, exportName(artifact)))
	}
	writeLine(r.stdout, "exported %s to %s", plural(len(refs), "artifact"), dir)
}
