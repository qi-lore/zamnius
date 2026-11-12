package domain

import (
	"errors"
	"fmt"
)

// Error is the classified error every layer returns. Class drives retry and
// budget decisions; Hint is mandatory for configuration and integrity failures
// because contracts/cli.md §2 requires the operator to be told what to fix.
type Error struct {
	Class ErrorClass
	Msg   string
	Hint  string
	Err   error
}

// Error implements the error interface, prefixing the class so a log line is
// diagnosable without unwrapping.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.Msg)
}

// Unwrap exposes the cause so errors.Is and errors.As reach the sentinels.
func (e *Error) Unwrap() error { return e.Err }

// ClassOf returns the classification carried by err, defaulting to internal.
// Every unclassified error is internal by contract (model-client.md §2.4).
func ClassOf(err error) ErrorClass {
	if err == nil {
		return ""
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Class
	}
	return ErrClassInternal
}

// HintOf returns the operator-facing hint carried by err, if any.
func HintOf(err error) string {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Hint
	}
	return ""
}

// Sentinel errors. They are *Error values so callers get a class for free while
// still matching with errors.Is, e.g. fmt.Errorf("%w: owner=%s", ErrRunAlreadyOwned, id).
var (
	// ErrRunAlreadyOwned means another live process holds the run's lease (FR-016).
	ErrRunAlreadyOwned = &Error{Class: ErrClassPermanent, Msg: "run is owned by another live process"}
	// ErrStaleRecord means a writer held an outdated record_version and must re-read.
	ErrStaleRecord = &Error{Class: ErrClassInternal, Msg: "record is stale"}
	// ErrIntegrity means persisted state is inconsistent with itself: a broken
	// event chain, a missing artifact file, or a fingerprint mismatch.
	ErrIntegrity = &Error{Class: ErrClassInternal, Msg: "persistence integrity failure"}
	// ErrInvalidDefinition means a workflow definition failed validation (FR-052).
	ErrInvalidDefinition = &Error{Class: ErrClassPermanent, Msg: "workflow definition is invalid"}
	// ErrInvalidConfig means engine configuration failed validation (FR-052).
	ErrInvalidConfig = &Error{Class: ErrClassPermanent, Msg: "configuration is invalid"}
	// ErrTerminalRun means the run already reached a terminal status (FR-018).
	ErrTerminalRun = &Error{Class: ErrClassPermanent, Msg: "run is already terminal"}
	// ErrDefinitionDrift means the definition supplied at resume differs from the
	// one the run was created with (data-model.md §10).
	ErrDefinitionDrift = &Error{Class: ErrClassPermanent, Msg: "definition does not match the run"}
	// ErrMigrationRefused means persisted state was written by an incompatible
	// schema version and cannot be interpreted (FR-017).
	ErrMigrationRefused = &Error{Class: ErrClassPermanent, Msg: "persisted state cannot be migrated"}
	// ErrNotFound means the addressed record does not exist.
	ErrNotFound = &Error{Class: ErrClassPermanent, Msg: "record not found"}
)
