package definition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/qi-lore/zamnius/internal/domain"
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

// Decode parses data in the given format and normalizes the result.
func Decode(data []byte, format Format) (Definition, error) {
	var def Definition
	switch format {
	case FormatYAML:
		if err := yaml.UnmarshalWithOptions(data, &def, yaml.Strict()); err != nil {
			return Definition{}, decodeError(err)
		}
	case FormatJSON:
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&def); err != nil {
			return Definition{}, decodeError(err)
		}
	case FormatTOML:
		decoder := toml.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&def); err != nil {
			return Definition{}, decodeError(err)
		}
	default:
		return Definition{}, &domain.Error{
			Class: domain.ErrClassPermanent,
			Msg:   "workflow definition is invalid",
			Hint:  fmt.Sprintf("format %q is not supported; accepted formats are yaml, json, toml", format),
			Err:   domain.ErrInvalidDefinition,
		}
	}
	return Normalize(def)
}

// FromSnapshot restores the definition a run was created with. The snapshot is
// canonical JSON, so resume never depends on the file the operator still has.
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
