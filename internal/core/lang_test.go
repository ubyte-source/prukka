package core_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestLangBaseStripsTheRegion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tag  core.Lang
		want core.Lang
	}{
		{name: "base only", tag: "en", want: "en"},
		{name: "regional", tag: "en-US", want: "en"},
		{name: "auto sentinel", tag: core.LangAuto, want: core.LangAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.tag.Base(); got != tc.want {
				t.Fatalf("Base(%q) = %q, want %q", tc.tag, got, tc.want)
			}
		})
	}
}

func TestSameLangMatchesBasesCaseInsensitively(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b core.Lang
		want bool
	}{
		{name: "mixed case regional", a: "EN-GB", b: "en", want: true},
		{name: "two regions", a: "en-US", b: "EN-GB", want: true},
		{name: "different bases", a: "it", b: "en", want: false},
		// Two auto sentinels compare equal — exactly why lane callers must
		// guard `source != core.LangAuto` before treating a pair as same-language.
		{name: "auto sentinels", a: core.LangAuto, b: core.LangAuto, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := core.SameLang(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameLang(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
