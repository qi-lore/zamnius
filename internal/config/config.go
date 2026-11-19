// Package config resolves engine settings with one documented precedence:
//
//	CLI flags > ZAMNIUS_* environment > explicit --config file > discovered config > defaults
//
// Decoding is strict in all three formats, and a refused value names the setting
// and the accepted values (FR-052, SC-012). Nothing here reaches a model or the
// network, so it is safe to call before a store exists.
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qi-lore/zamnius/internal/domain"
	"github.com/qi-lore/zamnius/internal/strict"
)

// Setting names, used verbatim in error hints so they match the file keys.
const (
	KeyStateDir       = "state_dir"
	KeyLogLevel       = "log_level"
	KeyLogFormat      = "log_format"
	KeyMaxConcurrency = "max_concurrency"
	KeyLeaseTTL       = "lease_ttl"
)

// Defaults are the built-in values: they are the last source in the precedence
// chain and therefore never in conflict with themselves. The execution defaults
// come from the domain package so the engine cannot enforce a different number
// than this surface defaults to (ADR-0010).
const (
	DefaultStateDir       = "./.zamnius"
	DefaultLogLevel       = "info"
	DefaultLogFormat      = "text"
	DefaultMaxConcurrency = domain.DefaultMaxConcurrency
	DefaultLeaseTTL       = domain.DefaultLeaseTTL
)

// MinLeaseTTL is the shortest ownership window this surface accepts. Renewal
// happens at a third of the TTL, so a shorter window would spend more time
// renewing than working and would invite a takeover of a live owner.
const MinLeaseTTL = time.Second

// Settings is the resolved configuration. Every field is concrete: callers never
// have to reason about which source won.
type Settings struct {
	StateDir       string
	LogLevel       string
	LogFormat      string
	MaxConcurrency int
	LeaseTTL       time.Duration
}

// File is the on-disk shape. Pointer fields distinguish "absent" from "set to the
// zero value", which is what makes precedence well defined.
type File struct {
	StateDir       *string `json:"state_dir" yaml:"state_dir" toml:"state_dir"`
	LogLevel       *string `json:"log_level" yaml:"log_level" toml:"log_level"`
	LogFormat      *string `json:"log_format" yaml:"log_format" toml:"log_format"`
	MaxConcurrency *int    `json:"max_concurrency" yaml:"max_concurrency" toml:"max_concurrency"`
	LeaseTTL       *string `json:"lease_ttl" yaml:"lease_ttl" toml:"lease_ttl"`
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
		LeaseTTL:       DefaultLeaseTTL,
	}

	// Layer order is inverted on purpose: later layers overwrite earlier ones, so
	// the highest precedence is applied last.
	discovered, err := loadDiscovered(ctx, sources.DiscoverDir)
	if err != nil {
		return Settings{}, err
	}
	if discovered != nil {
		if err := apply(discovered, &settings); err != nil {
			return Settings{}, err
		}
	}

	if sources.ExplicitFile != "" {
		fromFile, err := LoadFile(ctx, sources.ExplicitFile)
		if err != nil {
			return Settings{}, err
		}
		if err := apply(fromFile, &settings); err != nil {
			return Settings{}, err
		}
	}

	if err := applyEnvironment(sources.Environ, &settings); err != nil {
		return Settings{}, err
	}
	if err := applyOverrides(sources.Overrides, &settings); err != nil {
		return Settings{}, err
	}

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
		err = strict.YAML(data, file)
	case ".json":
		err = strict.JSON(data, file)
	case ".toml":
		err = strict.TOML(data, file)
	default:
		return nil, configError(path, fmt.Sprintf(
			"configuration file %q has an unsupported extension; accepted extensions are .yaml, .yml, .json, .toml", path))
	}
	if err != nil {
		return nil, configError(path, fmt.Sprintf("configuration file %q is invalid: %v", path, err))
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
	if s.LeaseTTL < MinLeaseTTL {
		return configError(KeyLeaseTTL, fmt.Sprintf(
			`setting "lease_ttl" must be at least %s; got %s`, MinLeaseTTL, s.LeaseTTL))
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
	if value, ok := values["ZAMNIUS_LEASE_TTL"]; ok {
		parsed, err := parseLeaseTTL(value, "environment variable ZAMNIUS_LEASE_TTL")
		if err != nil {
			return err
		}
		settings.LeaseTTL = parsed
	}
	return nil
}

// parseLeaseTTL reads a duration such as "45s" or "2m" and refuses anything else,
// naming the source so the hint points at the file or the variable to fix.
func parseLeaseTTL(value, source string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, configError(KeyLeaseTTL, fmt.Sprintf(
			`%s must be a duration such as 30s or 2m; got %q`, source, value))
	}
	return parsed, nil
}

func applyOverrides(overrides Overrides, settings *Settings) error {
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
	return nil
}

func apply(file *File, settings *Settings) error {
	if file == nil {
		return nil
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
	if file.LeaseTTL != nil {
		parsed, err := parseLeaseTTL(*file.LeaseTTL, `setting "lease_ttl"`)
		if err != nil {
			return err
		}
		settings.LeaseTTL = parsed
	}
	return nil
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
