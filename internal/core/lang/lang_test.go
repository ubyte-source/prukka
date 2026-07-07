package lang_test

import (
	"errors"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
)

func TestParseValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  core.Lang
	}{
		{name: "plain base", input: "it", want: "it"},
		{name: "uppercase base", input: "IT", want: "it"},
		{name: "surrounding space", input: "  en ", want: "en"},
		{name: "region lowercased input", input: "de-ch", want: "de-CH"},
		{name: "underscore separator", input: "de_CH", want: "de-CH"},
		{name: "mixed case region", input: "pt-Br", want: "pt-BR"},
		{name: "auto sentinel", input: "auto", want: core.LangAuto},
		{name: "auto sentinel uppercase", input: "AUTO", want: core.LangAuto},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := lang.Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.input, err)
			}

			if got != tc.want {
				t.Fatalf("Parse(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{
			name:    "country code with two candidates",
			input:   "ch",
			wantMsg: `unknown language "ch" — did you mean "zh" (Chinese) or "de-CH" (Swiss German)?`,
		},
		{
			name:    "country code with one candidate",
			input:   "jp",
			wantMsg: `unknown language "jp" — did you mean "ja" (Japanese)?`,
		},
		{
			name:    "spelled-out name",
			input:   "Italian",
			wantMsg: `unknown language "Italian" — did you mean "it" (Italian)?`,
		},
		{name: "no candidates", input: "xx", wantMsg: `unknown language "xx"`},
		{name: "empty tag", input: "", wantMsg: "unknown language: empty tag"},
		{name: "digit in base", input: "q1", wantMsg: `unknown language "q1"`},
		{
			name:    "three-letter region",
			input:   "de-CHE",
			wantMsg: `unknown language "de-CHE": invalid region subtag "CHE"`,
		},
		{
			name:    "too many subtags",
			input:   "de-CH-1996",
			wantMsg: `unknown language "de-CH-1996": at most one region subtag is supported`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := lang.Parse(tc.input)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", tc.input)
			}

			if !errors.Is(err, lang.ErrUnknown) {
				t.Fatalf("Parse(%q) error %v does not wrap ErrUnknown", tc.input, err)
			}

			if err.Error() != tc.wantMsg {
				t.Fatalf("Parse(%q) error = %q, want %q", tc.input, err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestParseList(t *testing.T) {
	t.Parallel()

	got, err := lang.ParseList("it, en ,de,it,,")
	if err != nil {
		t.Fatalf("ParseList returned error: %v", err)
	}

	want := []core.Lang{"it", "en", "de"}
	if len(got) != len(want) {
		t.Fatalf("ParseList = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseList = %v, want %v", got, want)
		}
	}

	if _, err := lang.ParseList("it,ch"); err == nil {
		t.Fatal("ParseList accepted invalid tag, want error")
	}
	if _, err := lang.ParseList("it,auto"); err == nil {
		t.Fatal("ParseList accepted auto as a target language")
	}
}

func TestAllIsACopy(t *testing.T) {
	t.Parallel()

	first := lang.All()
	if len(first) == 0 {
		t.Fatal("All returned an empty registry")
	}

	first[0] = lang.Language{Tag: "xx", Name: "Mutated", Native: "Mutated"}

	second := lang.All()
	if second[0].Tag == "xx" {
		t.Fatal("mutating the slice returned by All leaked into the registry")
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()

	cases := []struct{ tag, want string }{
		{tag: "it", want: "Italian (it)"},
		{tag: "de-CH", want: "German (de-CH)"},
		{tag: "xx", want: "xx"},
	}

	for _, tc := range cases {
		if got := lang.Describe(core.Lang(tc.tag)); got != tc.want {
			t.Fatalf("Describe(%s) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()

	l := lang.Language{Tag: "it", Name: "Italian", Native: "Italiano"}
	if got, want := l.Label(), "Italiano — it"; got != want {
		t.Fatalf("Label = %q, want %q", got, want)
	}
}

// FuzzParse feeds arbitrary strings to the language validator: it must
// never panic, and any tag it accepts must round-trip back to itself.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{"it", "en", "de-CH", "", "zz", "IT", "  it  ", "it-", "-it"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		tag, err := lang.Parse(in)
		if err != nil {
			return
		}

		if again, reErr := lang.Parse(string(tag)); reErr != nil || again != tag {
			t.Fatalf("accepted tag %q does not round-trip: (%q, %v)", tag, again, reErr)
		}
	})
}
