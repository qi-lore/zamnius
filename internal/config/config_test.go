package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/config"
	"github.com/qi-lore/zamnius/internal/domain"
)

func TestDefaultsWhenNothingIsSupplied(t *testing.T) {
	t.Parallel()

	settings, err := config.Load(context.Background(), config.Sources{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Settings{
		StateDir:       config.DefaultStateDir,
		LogLevel:       config.DefaultLogLevel,
		LogFormat:      config.DefaultLogFormat,
		MaxConcurrency: config.DefaultMaxConcurrency,
	}
	if settings != want {
		t.Errorf("settings = %+v, want %+v", settings, want)
	}
}

func TestPrecedenceFlagsOverEnvOverFileOverDiscovered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	discovered := filepath.Join(dir, "zamnius.yaml")
	writeFile(t, discovered, "state_dir: /from-discovered\nlog_level: warn\n")

	explicit := filepath.Join(dir, "explicit.json")
	writeFile(t, explicit, `{"state_dir":"/from-file","max_concurrency":4}`)

	settings, err := config.Load(context.Background(), config.Sources{
		DiscoverDir:  dir,
		ExplicitFile: explicit,
		Environ:      []string{"ZAMNIUS_STATE_DIR=/from-env", "ZAMNIUS_LOG_FORMAT=json"},
		Overrides:    config.Overrides{LogLevel: strPtr("debug")},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if settings.StateDir != "/from-env" {
		t.Errorf("state_dir = %q, want the environment value", settings.StateDir)
	}
	if settings.LogLevel != "debug" {
		t.Errorf("log_level = %q, want the flag value", settings.LogLevel)
	}
	if settings.LogFormat != "json" {
		t.Errorf("log_format = %q, want the environment value", settings.LogFormat)
	}
	if settings.MaxConcurrency != 4 {
		t.Errorf("max_concurrency = %d, want the explicit file value", settings.MaxConcurrency)
	}
}

func TestExplicitFileOverridesDiscovered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "zamnius.yaml"), "state_dir: /discovered\n")
	explicit := filepath.Join(dir, "other.toml")
	writeFile(t, explicit, "state_dir = \"/explicit\"\nlog_format = \"json\"\n")

	settings, err := config.Load(context.Background(), config.Sources{DiscoverDir: dir, ExplicitFile: explicit})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if settings.StateDir != "/explicit" {
		t.Errorf("state_dir = %q, want /explicit", settings.StateDir)
	}
}

func TestMultipleDiscoveredFilesAreRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "zamnius.yaml"), "state_dir: /a\n")
	writeFile(t, filepath.Join(dir, "zamnius.toml"), "state_dir = \"/b\"\n")

	_, err := config.Load(context.Background(), config.Sources{DiscoverDir: dir})
	if err == nil {
		t.Fatal("Load accepted two discovered config files")
	}
	if !strings.Contains(domain.HintOf(err), "multiple configuration files") {
		t.Errorf("hint = %q, want it to explain the conflict", domain.HintOf(err))
	}
}

func TestUnknownFieldIsRefusedInEveryFormat(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"bad.yaml": "state_dir: /a\nlog_levek: info\n",
		"bad.json": `{"state_dir":"/a","log_levek":"info"}`,
		"bad.toml": "state_dir = \"/a\"\nlog_levek = \"info\"\n",
	}
	for name, content := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), name)
			writeFile(t, path, content)
			if _, err := config.LoadFile(context.Background(), path); err == nil {
				t.Fatalf("%s accepted an unknown field", name)
			}
		})
	}
}

func TestDuplicateKeysInOneFileAreRefused(t *testing.T) {
	t.Parallel()

	// JSON silently keeps the last value, which is why duplicates are scanned for.
	path := filepath.Join(t.TempDir(), "dup.json")
	writeFile(t, path, `{"state_dir":"/a","state_dir":"/b"}`)
	_, err := config.LoadFile(context.Background(), path)
	if err == nil {
		t.Fatal("duplicate JSON keys were accepted")
	}
	if !strings.Contains(domain.HintOf(err), "duplicate key") {
		t.Errorf("hint = %q, want it to name the duplicate", domain.HintOf(err))
	}

	dupTOML := filepath.Join(t.TempDir(), "dup.toml")
	writeFile(t, dupTOML, "state_dir = \"/a\"\nstate_dir = \"/b\"\n")
	if _, err := config.LoadFile(context.Background(), dupTOML); err == nil {
		t.Fatal("duplicate TOML keys were accepted")
	}
}

func TestInvalidValuesNameTheSettingAndAcceptedValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source config.Sources
		want   string
	}{
		{
			name:   "log level",
			source: config.Sources{Environ: []string{"ZAMNIUS_LOG_LEVEL=verbose"}},
			want:   `setting "log_level" accepts debug, info, warn, error; got "verbose"`,
		},
		{
			name:   "log format",
			source: config.Sources{Environ: []string{"ZAMNIUS_LOG_FORMAT=yaml"}},
			want:   `setting "log_format" accepts text, json; got "yaml"`,
		},
		{
			name:   "concurrency",
			source: config.Sources{Overrides: config.Overrides{MaxConcurrency: intPtr(0)}},
			want:   `setting "max_concurrency" must be >= 1; got 0`,
		},
		{
			name:   "not an integer",
			source: config.Sources{Environ: []string{"ZAMNIUS_MAX_CONCURRENCY=lots"}},
			want:   "ZAMNIUS_MAX_CONCURRENCY must be an integer",
		},
		{
			name:   "empty state dir",
			source: config.Sources{Overrides: config.Overrides{StateDir: strPtr("  ")}},
			want:   `setting "state_dir" must not be empty`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(context.Background(), tc.source)
			if err == nil {
				t.Fatal("Load accepted an invalid value")
			}
			if !strings.Contains(domain.HintOf(err), tc.want) {
				t.Errorf("hint = %q, want it to contain %q", domain.HintOf(err), tc.want)
			}
			if got := domain.ClassOf(err); got != domain.ErrClassPermanent {
				t.Errorf("class = %q, want permanent", got)
			}
		})
	}
}

func TestUnsupportedExtensionIsRefused(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.ini")
	writeFile(t, path, "state_dir=/a\n")
	_, err := config.LoadFile(context.Background(), path)
	if err == nil {
		t.Fatal("accepted an unsupported extension")
	}
	if !strings.Contains(domain.HintOf(err), "accepted extensions") {
		t.Errorf("hint = %q", domain.HintOf(err))
	}
}

func TestMissingFileIsRefusedWithHint(t *testing.T) {
	t.Parallel()

	_, err := config.LoadFile(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("accepted a missing file")
	}
	if domain.HintOf(err) == "" {
		t.Error("missing configuration must carry a hint naming the file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func strPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }
