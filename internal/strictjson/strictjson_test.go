package strictjson_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/strictjson"
)

type doc struct {
	Name string `json:"name"`
	Meta struct {
		Kind string `json:"kind"`
	} `json:"meta"`
	Tags []string `json:"tags"`
}

func TestDecodeAcceptsOneCleanDocument(t *testing.T) {
	t.Parallel()

	var d doc
	input := `{"name":"a","meta":{"kind":"x"},"tags":["one","two"]}`
	if err := strictjson.Decode([]byte(input), &d); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Name != "a" || d.Meta.Kind != "x" || len(d.Tags) != 2 {
		t.Fatalf("decoded = %+v", d)
	}
}

func TestDecodeRejectsEveryLenientShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unknown field", input: `{"name":"a","bogus":1}`, want: "unknown field"},
		{name: "trailing data", input: `{"name":"a"} {"name":"b"}`, want: "trailing data"},
		{name: "top-level duplicate key", input: `{"name":"a","name":"b"}`, want: `duplicate object key "name"`},
		{name: "nested duplicate key", input: `{"meta":{"kind":"x","kind":"y"}}`, want: `duplicate object key "kind"`},
		{
			name:  "duplicate after nested containers",
			input: `{"tags":[],"meta":{"kind":"x"},"name":"a","tags":[]}`,
			want:  `duplicate object key "tags"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var d doc
			err := strictjson.Decode([]byte(tc.input), &d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Decode(%s) = %v, want %q", tc.input, err, tc.want)
			}
		})
	}
}

// Sibling keys in different objects are not duplicates: each object carries
// its own key set.
func TestDecodeAllowsRepeatedKeysAcrossObjects(t *testing.T) {
	t.Parallel()

	var v []map[string]string
	if err := strictjson.Decode([]byte(`[{"k":"1"},{"k":"2"}]`), &v); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}
