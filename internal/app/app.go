// Package app is the composition root. It is the only package that knows which
// implementation backs each port: configuration becomes a state directory, the
// state directory becomes a SQLite store and an artifact root, and the model
// registry becomes the engine's provider resolution. Business logic stays in the
// engine; this package only assembles.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/prompt-fu/zamnius/internal/config"
	"github.com/prompt-fu/zamnius/internal/domain"
	"github.com/prompt-fu/zamnius/internal/model"
	"github.com/prompt-fu/zamnius/internal/model/openai"
	"github.com/prompt-fu/zamnius/internal/observability"
	"github.com/prompt-fu/zamnius/internal/store/artifactfs"
	"github.com/prompt-fu/zamnius/internal/store/sqlite"
	"github.com/prompt-fu/zamnius/pkg/zamnius"
)

// Provider is the single provider name a definition may carry (data-model.md §1).
// The registry maps it to the reference adapter.
const Provider = "openai-compatible"

// BaseURLEnvVar names the environment variable the reference adapter reads its
// provider root from. It is not a secret, so it lives here with the composition
// that consumes it rather than with the redaction helpers.
const BaseURLEnvVar = "ZAMNIUS_MODEL_BASE_URL"

// dirMode keeps the state directory owner-only: it holds the audit record, and a
// world-readable trail is a disclosure risk (FR-054).
const dirMode = 0o750

// Paths are the files under a state directory. The names are the layout, not a
// detail: recovery is "open the store and resume", so an operator must be able to
// find the store without reading code.
type Paths struct {
	// Dir is the state directory itself.
	Dir string
	// Database is the SQLite file holding runs, phases, attempts, events, and
	// decisions.
	Database string
	// Artifacts is the content-addressed artifact root.
	Artifacts string
}

// PathsFor returns the layout of a state directory.
func PathsFor(dir string) Paths {
	return Paths{
		Dir:       dir,
		Database:  filepath.Join(dir, "zamnius.db"),
		Artifacts: filepath.Join(dir, "artifacts"),
	}
}

// App is an assembled process: resolved settings, the two stores, and the
// logger. The engine is built on demand so a read-only command (status,
// artifacts) never needs a model credential.
type App struct {
	Settings  config.Settings
	Paths     Paths
	Logger    *slog.Logger
	Store     *sqlite.Store
	Artifacts *artifactfs.Store
}

// Open resolves settings into adapters. It creates the state directory and opens
// both stores; every failure is an invalid-configuration error naming what to fix,
// because a half-open process would otherwise fail later with no context.
func Open(ctx context.Context, settings config.Settings) (*App, error) {
	if err := os.MkdirAll(PathsFor(settings.StateDir).Artifacts, dirMode); err != nil {
		return nil, configError(
			fmt.Sprintf("cannot create the state directory %s", settings.StateDir),
			"pass --state-dir (or set ZAMNIUS_STATE_DIR) to a directory this process can write",
			err,
		)
	}

	logger, err := observability.NewLogger(os.Stderr, settings.LogLevel, settings.LogFormat, observability.SecretValues())
	if err != nil {
		return nil, err
	}

	paths := PathsFor(settings.StateDir)
	store, err := sqlite.Open(ctx, paths.Database)
	if err != nil {
		return nil, configError(
			fmt.Sprintf("cannot open the state store %s", paths.Database),
			"check that the file is a zamnius state store and that this process may write it",
			err,
		)
	}

	artifactStore, err := artifactfs.Open(paths.Artifacts)
	if err != nil {
		_ = store.Close()
		return nil, configError(
			fmt.Sprintf("cannot open the artifact root %s", paths.Artifacts),
			"check that the directory exists and is writable by this process",
			err,
		)
	}

	return &App{
		Settings:  settings,
		Paths:     paths,
		Logger:    logger,
		Store:     store,
		Artifacts: artifactStore,
	}, nil
}

// Close releases the state store. Artifacts are files, so there is nothing to
// close for them.
func (a *App) Close() error { return a.Store.Close() }

// Engine builds the engine over the opened stores, constructing the reference
// model adapter from the environment. It is called only by commands that execute
// work, so a missing credential fails those commands and leaves inspection
// working: an operator must be able to read a run whose key has since expired.
func (a *App) Engine() (*zamnius.Engine, error) {
	client, err := openai.New(openai.Options{
		BaseURL: os.Getenv(BaseURLEnvVar),
		Logger:  a.Logger,
	})
	if err != nil {
		hint := domain.HintOf(err)
		if hint == "" {
			hint = fmt.Sprintf("set %s (or %s) in the environment", observability.SecretEnvVar, BaseURLEnvVar)
		}
		return nil, configError("the model adapter cannot be constructed", hint, err)
	}

	registry := registryAdapter{inner: model.NewRegistry().Register(Provider, client)}
	engine, err := zamnius.New(zamnius.Options{
		Store:     a.Store,
		Artifacts: a.Artifacts,
		Models:    registry,
		Logger:    a.Logger,
	})
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// Inspector builds an engine for read-only commands. It resolves no provider, so a
// run stays readable after the credential that produced it has expired; any attempt
// to execute work fails with the credential error rather than silently substituting
// a fake model.
func (a *App) Inspector() (*zamnius.Engine, error) {
	engine, err := zamnius.New(zamnius.Options{
		Store:     a.Store,
		Artifacts: a.Artifacts,
		Models:    inspectorRegistry{},
		Logger:    a.Logger,
	})
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// inspectorRegistry satisfies the model port for inspection and refuses to
// resolve anything: a read-only command must never be able to run work by accident.
type inspectorRegistry struct{}

// Client implements zamnius.ModelRegistry.
func (inspectorRegistry) Client(provider string) (zamnius.ModelClient, error) {
	return nil, &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   fmt.Sprintf("provider %q is not resolvable for a read-only command", provider),
		Hint:  fmt.Sprintf("set %s to execute work", observability.SecretEnvVar),
	}
}

// registryAdapter exposes the concrete registry through the public contract. The
// two interfaces differ only in the name of the client type they return, which Go
// treats as unrelated, so the bridge is explicit rather than implicit.
type registryAdapter struct {
	inner *model.Registry
}

// Client implements zamnius.ModelRegistry.
func (a registryAdapter) Client(provider string) (zamnius.ModelClient, error) {
	client, err := a.inner.Client(provider)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// configError classifies a composition failure as invalid configuration, joining
// the sentinel so errors.Is works and both hints survive unwrapping.
func configError(msg, hint string, cause error) error {
	return &domain.Error{
		Class: domain.ErrClassPermanent,
		Msg:   msg,
		Hint:  hint,
		Err:   errors.Join(domain.ErrInvalidConfig, cause),
	}
}
