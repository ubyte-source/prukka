package lang

import (
	"sort"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

// TestRegistryIntegrity: unique lowercase tags, full names, stable
// English-name order.
func TestRegistryIntegrity(t *testing.T) {
	t.Parallel()

	seen := map[core.Lang]bool{}
	names := make([]string, 0, len(registry))

	for _, l := range registry {
		if l.Tag == "" || l.Name == "" || l.Native == "" {
			t.Fatalf("incomplete entry: %+v", l)
		}

		if seen[l.Tag] {
			t.Fatalf("duplicate tag %q", l.Tag)
		}

		seen[l.Tag] = true
		names = append(names, l.Name)
	}

	if !sort.StringsAreSorted(names) {
		t.Fatal("registry is not ordered by English name")
	}
}

// TestConfusionsResolveToValidTags: a hint pointing at a rejected tag
// would send the user in a circle.
func TestConfusionsResolveToValidTags(t *testing.T) {
	t.Parallel()

	for input, suggestions := range confusions {
		if _, err := Parse(input); err == nil {
			t.Fatalf("confusion key %q is itself a valid tag", input)
		}

		for _, s := range suggestions {
			if _, err := Parse(string(s.Tag)); err != nil {
				t.Fatalf("confusion %q suggests rejected tag %q: %v", input, s.Tag, err)
			}
		}
	}
}
