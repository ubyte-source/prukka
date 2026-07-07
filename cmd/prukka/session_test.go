package main

import (
	"testing"
)

func TestSplitLangArgs(t *testing.T) {
	t.Parallel()

	add, remove, err := splitLangArgs([]string{"+fr", "-de", "+en"})
	if err != nil {
		t.Fatalf("splitLangArgs returned error: %v", err)
	}

	if len(add) != 2 || add[0] != "fr" || add[1] != "en" {
		t.Fatalf("add = %v, want [fr en]", add)
	}

	if len(remove) != 1 || remove[0] != "de" {
		t.Fatalf("remove = %v, want [de]", remove)
	}

	// A change without +/- is rejected.
	if _, _, err := splitLangArgs([]string{"fr"}); err == nil {
		t.Fatal("splitLangArgs accepted an unprefixed change")
	}

	// An invalid tag surfaces the registry error.
	if _, _, err := splitLangArgs([]string{"+nope"}); err == nil {
		t.Fatal("splitLangArgs accepted an invalid language")
	}
}
