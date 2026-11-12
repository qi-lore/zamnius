// Package config resolves engine settings with one documented precedence:
//
//	CLI flags > ZAMNIUS_* environment > explicit --config file > discovered config > defaults
//
// Decoding is strict in all three formats, and a refused value names the setting
// and the accepted values (FR-052, SC-012). Nothing here reaches a model or the
// network, so it is safe to call before a store exists.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/hIMEI29A/zamnius/internal/domain"
)

// Setting names, used verbatim in error hints so they match the file keys.
const (
	KeyStateDir       = "state_dir"
	KeyLogLevel       = "log_level"
	KeyLogFormat      = "log_format"
	KeyMaxConcurrency = "max_concurrency"
)

// Defaults are the built-in values: they are the last source in the precedence
// chain and therefore never in conflict with themselves.
const (
	DefaultStateDir       = "./.zamnius"
	DefaultLogLevel       = "info"
	DefaultLogFormat      = "text"
	DefaultMaxConcurrency = 8
)

// Settings is the resolved configuration. Every field is concrete: callers never
// have to reason about which source won.
type Settings struct {
	StateDir       string
	LogLevel       string
	LogFormat      string
	MaxConcurrency int
}

// File is the on-disk shape. Pointer fields distinguish "absent" from "set to the
// zero value", which is what makes precedence well defined.
type File struct {
	StateDir       *string `json:"state_dir" yaml:"state_dir" toml:"state_dir"`
	LogLevel       *string `json:"log_level" yaml:"log_level" toml:"log_level"`
	LogFormat      *string `json:"log_format" yaml:"log_format" toml:"log_format"`
	MaxConcurrency *int    `json:"max_concurrency" yaml:"max_concurrency" toml:"max_concurrency"`
}

// Overrides carries the highest-precedence values. Nil means "not supplied on the
// command line", which lets the next source answer.
type Overrides struct {
	StateDir       *string
	LogLevel       *string
	LogFormat      *string
	MaxConcurrency *int
}

// Sources is everything Load may consult.
type Sources struct {
	// Overrides are CLI flags.
	Overrides Overrides
	// Environ is usually os.Environ(); a nil slice means "no environment".
	Environ []string
	// ExplicitFile is the --config path, if any.
	ExplicitFile string
	// DiscoverDir is searched for zamnius.yaml / .zamnius.yaml / zamnius.json / zamnius.toml.
	DiscoverDir string
}

// DiscoveredNames are the config file names searched in DiscoverDir, in the order
// they are reported. Finding more than one is an error: silently picking one
// would make behaviour depend on directory listing order.
var DiscoveredNames = []string{"zamnius.yaml", ".zamnius.yaml", "zamnius.json", "zamnius.toml"}

// Load resolves settings. It refuses invalid values instead of defaulting them.
func Load(ctx context.Context, sources Sources) (Settings, error) {
	settings := Settings{
		StateDir:       DefaultStateDir,
		LogLevel:       DefaultLogLevel,
		LogFormat:      DefaultLogFormat,
		MaxConcurrency: DefaultMaxConcurrency,
	}

	// Layer order is inverted on purpose: later layers overwrite earlier ones, so
	// the highest precedence is applied last.
	discovered, err := loadDiscovered(ctx, sources.DiscoverDir)
	if err != nil {
		return Settings{}, err
	}
	if discovered != nil {
		apply(discovered, &settings)
	}

	if sources.ExplicitFile != "" {
		fromFile, err := LoadFile(ctx, sources.ExplicitFile)
		if err != nil {
			return Settings{}, err
		}
		apply(fromFile, &settings)
	}

	if err := applyEnvironment(sources.Environ, &settings); err != nil {
		return Settings{}, err
	}
	applyOverrides(sources.Overrides, &settings)

	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// LoadFile decodes one explicitly named file, choosing the decoder by extension.
func LoadFile(ctx context.Context, path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, configError(path, fmt.Sprintf("configuration file %q could not be read: %v", path, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}

	file := &File{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.UnmarshalWithOptions(data, file, yaml.Strict()); err != nil {
			return nil, configError(path, fmt.Sprintf("configuration file %q is invalid: %v", path, err))
		}
	case ".json":
		if err := rejectDuplicateJSONKeys(data); err != nil {
			return nil, configError(path, fmt.Sprintf("configuration file %q is invalid: %v", path, err))
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(file); err != nil {
			return nil, configError(path, fmt.Sprintf("configuration file %q is invalid: %v", path, err))
		}
	case ".toml":
		decoder := toml.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(file); err != nil {
			return nil, configError(path, fmt.Sprintf("configuration file %q is invalid: %v", path, err))
		}
	default:
		return nil, configError(path, fmt.Sprintf(
			"configuration file %q has an unsupported extension; accepted extensions are .yaml, .yml, .json, .toml", path))
	}
	return file, nil
}

// Validate checks every setting and reports the first offending one. Hints carry
// both the accepted values and the received value so SC-012's assertion is exact.
func (s Settings) Validate() error {
	if strings.TrimSpace(s.StateDir) == "" {
		return configError(KeyStateDir, `setting "state_dir" must not be empty`)
	}
	switch strings.ToLower(s.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return configError(KeyLogLevel, fmt.Sprintf(
			`setting "log_level" accepts debug, info, warn, error; got %q`, s.LogLevel))
	}
	switch strings.ToLower(s.LogFormat) {
	case "text", "json":
	default:
		return configError(KeyLogFormat, fmt.Sprintf(
			`setting "log_format" accepts text, json; got %q`, s.LogFormat))
	}
	if s.MaxConcurrency < 1 {
		return configError(KeyMaxConcurrency, fmt.Sprintf(
			`setting "max_concurrency" must be >= 1; got %d`, s.MaxConcurrency))
	}
	return nil
}

func loadDiscovered(ctx context.Context, dir string) (*File, error) {
	if dir == "" {
		return nil, nil
	}
	var found []string
	for _, name := range DiscoveredNames {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return LoadFile(ctx, found[0])
	default:
		return nil, configError("config", fmt.Sprintf(
			"multiple configuration files found in %q: %s; keep exactly one or pass --config",
			dir, strings.Join(found, ", ")))
	}
}

func applyEnvironment(environ []string, settings *Settings) error {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[key] = value
	}

	if value, ok := values["ZAMNIUS_STATE_DIR"]; ok {
		settings.StateDir = value
	}
	if value, ok := values["ZAMNIUS_LOG_LEVEL"]; ok {
		settings.LogLevel = value
	}
	if value, ok := values["ZAMNIUS_LOG_FORMAT"]; ok {
		settings.LogFormat = value
	}
	if value, ok := values["ZAMNIUS_MAX_CONCURRENCY"]; ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return configError(KeyMaxConcurrency, fmt.Sprintf(
				`environment variable ZAMNIUS_MAX_CONCURRENCY must be an integer; got %q`, value))
		}
		settings.MaxConcurrency = parsed
	}
	return nil
}

func applyOverrides(overrides Overrides, settings *Settings) {
	if overrides.StateDir != nil {
		settings.StateDir = *overrides.StateDir
	}
	if overrides.LogLevel != nil {
		settings.LogLevel = *overrides.LogLevel
	}
	if overrides.LogFormat != nil {
		settings.LogFormat = *overrides.LogFormat
	}
	if overrides.MaxConcurrency != nil {
		settings.MaxConcurrency = *overrides.MaxConcurrency
	}
}

func apply(file *File, settings *Settings) {
	if file == nil {
		return
	}
	if file.StateDir != nil {
		settings.StateDir = *file.StateDir
	}
	if file.LogLevel != nil {
		settings.LogLevel = *file.LogLevel
	}
	if file.LogFormat != nil {
		settings.LogFormat = *file.LogFormat
	}
	if file.MaxConcurrency != nil {
		settings.MaxConcurrency = *file.MaxConcurrency
	}
}

// configError builds the classified, hinted error every caller reports verbatim.
// The hint must name the offending setting or file: SC-012 is asserted on it.
func configError(setting, message string) error {
	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   "configuration is invalid",
		Hint:  message,
		Err:   fmt.Errorf("%w: %s", domain.ErrInvalidConfig, setting),
	}
}

// rejectDuplicateJSONKeys walks the token stream looking for a repeated key in the
// same object. stdlib json silently keeps the last value, which would make a
// duplicated setting behave differently from YAML and TOML.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSON(decoder); err != nil {
		return err
	}
	return nil
}

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("parse json object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("parse json: object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q in one object", key)
			}
			seen[key] = true
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("parse json: %w", err)
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("parse json: %w", err)
		}
	}
	return nil
}
