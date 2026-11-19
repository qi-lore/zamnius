package strict_test

import (
	"strings"
	"testing"

	"github.com/qi-lore/zamnius/internal/strict"
)

// sample is the shape both surfaces decode: a scalar, a nested object, and a list.
type sample struct {
	Name  string `json:"name" yaml:"name" toml:"name"`
	Nest  *nest  `json:"nest,omitempty" yaml:"nest,omitempty" toml:"nest,omitempty"`
	Items []item `json:"items,omitempty" yaml:"items,omitempty" toml:"items,omitempty"`
}

type nest struct {
	Count int `json:"count" yaml:"count" toml:"count"`
}

type item struct {
	Label string `json:"label" yaml:"label" toml:"label"`
}

func TestEachFormatDecodesAValidDocument(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		decode func([]byte, any) error
		doc    string
	}{
		{"yaml", strict.YAML, "name: plan\nnest:\n  count: 2\nitems:\n  - label: one\n"},
		{"json", strict.JSON, `{"name":"plan","nest":{"count":2},"items":[{"label":"one"}]}`},
		{"toml", strict.TOML, "name = \"plan\"\n\n[nest]\ncount = 2\n\n[[items]]\nlabel = \"one\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got sample
			if err := tc.decode([]byte(tc.doc), &got); err != nil {
				t.Fatalf("decode() error = %v", err)
			}
			if got.Name != "plan" || got.Nest == nil || got.Nest.Count != 2 ||
				len(got.Items) != 1 || got.Items[0].Label != "one" {
				t.Fatalf("decoded %+v, want the document's values", got)
			}
		})
	}
}

// TestUnknownFieldsAreRefusedInEveryFormat is the guarantee the two surfaces share:
// a misspelled key is an error with a name in it, never a silently ignored setting.
func TestUnknownFieldsAreRefusedInEveryFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		decode func([]byte, any) error
		doc    string
	}{
		{"yaml", strict.YAML, "name: plan\nnmae: typo\n"},
		{"json", strict.JSON, `{"name":"plan","nmae":"typo"}`},
		{"toml", strict.TOML, "name = \"plan\"\nnmae = \"typo\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got sample
			err := tc.decode([]byte(tc.doc), &got)
			if err == nil {
				t.Fatalf("decode() accepted an unknown field")
			}
			if !strings.Contains(err.Error(), "nmae") {
				t.Errorf("error %q does not name the offending key", err)
			}
		})
	}
}

// TestDuplicateJSONKeysAreRefused covers the divergence this package exists to
// remove: stdlib json keeps the last value, so {"count":1,"count":2} would decode
// as 2 in one surface and be refused in the other.
func TestDuplicateJSONKeysAreRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"top level", `{"name":"a","name":"b"}`, `duplicate key "name"`},
		{"nested object", `{"nest":{"count":1,"count":2}}`, `duplicate key "count"`},
		{"inside array element", `{"items":[{"label":"a","label":"b"}]}`, `duplicate key "label"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got sample
			err := strict.JSON([]byte(tc.doc), &got)
			if err == nil {
				t.Fatalf("decode() accepted a duplicate key")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
