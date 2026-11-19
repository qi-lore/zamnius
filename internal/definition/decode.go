package definition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/strict"
)

// Format identifies a declarative definition encoding.
type Format string

// Supported encodings (FR-051, research.md R4). All three decode into the same
// struct with strict field handling, so an unknown key is an error everywhere
// rather than only in some formats.
const (
	FormatYAML Format = "yaml"
	FormatJSON Format = "json"
	FormatTOML Format = "toml"
)

// FormatForPath maps a file extension onto a format.
func FormatForPath(path string) (Format, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return FormatYAML, nil
	case ".json":
		return FormatJSON, nil
	case ".toml":
		return FormatTOML, nil
	default:
		return "", &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "workflow definition is invalid",
			Hint:  fmt.Sprintf("file %q has an unsupported extension; accepted extensions are .yaml, .yml, .json, .toml", path),
			Err:   domain.ErrInvalidDefinition,
		}
	}
}

// LoadFile reads, decodes, and normalizes a definition from disk.
func LoadFile(ctx context.Context, path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "workflow definition could not be read",
			Hint:  fmt.Sprintf("file %q: %v", path, err),
			Err:   domain.ErrInvalidDefinition,
		}
	}
	if err := ctx.Err(); err != nil {
		return Definition{}, fmt.Errorf("load definition %s: %w", path, err)
	}
	format, err := FormatForPath(path)
	if err != nil {
		return Definition{}, err
	}
	return Decode(data, format)
}

// Decode parses data in the given format and normalizes the result. Decoding is
// strict through internal/strict, so an unknown key or a duplicated key is refused
// exactly as it is in a process-settings file (ADR-0010).
func Decode(data []byte, format Format) (Definition, error) {
	var def Definition
	var err error
	switch format {
	case FormatYAML:
		err = strict.YAML(data, &def)
	case FormatJSON:
		err = strict.JSON(data, &def)
	case FormatTOML:
		err = strict.TOML(data, &def)
	default:
		return Definition{}, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "workflow definition is invalid",
			Hint:  fmt.Sprintf("format %q is not supported; accepted formats are yaml, json, toml", format),
			Err:   domain.ErrInvalidDefinition,
		}
	}
	if err != nil {
		return Definition{}, decodeError(err)
	}
	return Normalize(def)
}

// FromSnapshot restores the definition a run was created with. The snapshot is
// canonical JSON written by this program, so resume never depends on the file the
// operator still has. It stays strict — a snapshot is not operator input, and a
// field this build no longer knows is evidence of corruption, not of a typo.
func FromSnapshot(snapshot []byte) (Definition, error) {
	var def Definition
	decoder := json.NewDecoder(bytes.NewReader(snapshot))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&def); err != nil {
		return Definition{}, &domain.Error{
			Class: domain.ErrClassInternal,
			Msg:   "stored definition snapshot could not be read",
			Hint:  fmt.Sprintf("snapshot is corrupt: %v", err),
			Err:   domain.ErrIntegrity,
		}
	}
	return def, nil
}

func decodeError(cause error) error {
	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "workflow definition is invalid",
		Hint:  cause.Error(),
		Err:   domain.ErrInvalidDefinition,
	}
}
