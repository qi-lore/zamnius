// Package strict decodes configuration documents in the three formats Zamnius
// accepts, with one guarantee set: an unknown field is refused, and a duplicate
// key in one JSON object is refused instead of silently keeping the last value.
//
// Both configuration surfaces use it — process settings and workflow definitions —
// so a document that is refused in one cannot be quietly accepted in the other
// (ADR-0010). The package only decodes; what a document means stays with the
// package that owns the vocabulary.
package strict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	toml "github.com/pelletier/go-toml/v2"
)

// YAML decodes one YAML document into target, refusing unknown fields.
func YAML(data []byte, target any) error {
	return yaml.UnmarshalWithOptions(data, target, yaml.Strict())
}

// JSON decodes one JSON document into target, refusing unknown fields and
// duplicate keys.
func JSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// TOML decodes one TOML document into target, refusing unknown fields. The
// library reports strict-mode violations as one opaque sentence, so the offending
// keys are lifted out of it: a hint is useless if it does not name the key.
func TOML(data []byte, target any) error {
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	var missing *toml.StrictMissingError
	if errors.As(err, &missing) {
		return unknownKeysError(missing)
	}
	return err
}

func unknownKeysError(missing *toml.StrictMissingError) error {
	keys := make([]string, 0, len(missing.Errors))
	for i := range missing.Errors {
		keys = append(keys, strings.Join(missing.Errors[i].Key(), "."))
	}
	return fmt.Errorf("toml: unknown keys %s", strings.Join(keys, ", "))
}

// rejectDuplicateJSONKeys walks the token stream looking for a repeated key in the
// same object. stdlib json silently keeps the last value, which would make a
// duplicated key behave differently from YAML and TOML — and would let a document
// that looks like it sets one value silently set another.
func rejectDuplicateJSONKeys(data []byte) error {
	return walkJSON(json.NewDecoder(bytes.NewReader(data)))
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
