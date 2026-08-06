package realtime

import (
	"reflect"
	"testing"
)

func partial(text string) Transcript { return Transcript{Text: text} }

func final(text string) Transcript { return Transcript{Text: text, Stable: true, Final: true} }

func TestCommitFinalCutsAndFlushes(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	got := c.commit(final("Va bene davvero, ci vediamo."))
	want := []string{"Va bene davvero,", "ci vediamo."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitFinalHoldsTinyLeadingClause(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	// The comma sits below minClause, so the whole line commits as one clause.
	got := c.commit(final("Va bene, ci vediamo domani."))
	want := []string{"Va bene, ci vediamo domani."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitForcesClauseAtMaxWords(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	got := c.commit(final("uno due tre quattro cinque sei sette otto nove"))
	want := []string{"uno due tre quattro cinque sei sette otto", "nove"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitLocalAgreementCommitsStablePrefix(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	if got := c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci")); got != nil {
		t.Fatalf("first partial committed %v, want nothing", got)
	}

	// Agreement reaches ten; hold-k withholds two, so maxClause cuts at eight.
	got := c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci"))
	want := []string{"uno due tre quattro cinque sei sette otto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCallCommitterReleasesShortAgreedTurns(t *testing.T) {
	t.Parallel()

	c := newCommitter(true)
	first := "uno due tre quattro. cinque"
	if got := c.commit(partial(first)); got != nil {
		t.Fatalf("first call partial committed %v", got)
	}
	got := c.commit(partial(first))
	want := []string{"uno due tre quattro."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fast-turn clauses = %v, want %v", got, want)
	}
}

func TestCommitAgreementIgnoresCaseAndEdgePunctuation(t *testing.T) {
	t.Parallel()

	c := newCommitter(true)
	if got := c.commit(partial("ciao mondo come va. oggi")); got != nil {
		t.Fatalf("first partial committed %v", got)
	}
	got := c.commit(partial("Ciao, mondo come va. oggi"))
	want := []string{"Ciao, mondo come va."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("surface revision clauses = %v, want %v", got, want)
	}
	if wordsAgree(",", ".") {
		t.Fatal("standalone punctuation tokens agreed")
	}
}

func TestCallFinalPreservesSentenceContextForTranslation(t *testing.T) {
	t.Parallel()

	c := newCommitter(true)
	for _, sentence := range []string{
		"Hello, this is the incoming caller.",
		"The translated voice should be clear and understandable.",
	} {
		if got := c.commit(final(sentence)); !reflect.DeepEqual(got, []string{sentence}) {
			t.Fatalf("call final %q split into %v", sentence, got)
		}
	}
}

func TestEmptyFinalResetsAgreementEpoch(t *testing.T) {
	t.Parallel()

	c := newCommitter(true)
	c.commit(partial("uno due tre quattro cinque"))
	c.commit(final(""))
	if got := c.commit(partial("uno due tre quattro cinque")); got != nil {
		t.Fatalf("new utterance agreed with the closed epoch: %v", got)
	}
}

func TestCommitWithholdsRevisedTail(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	c.commit(partial("alfa bravo charlie delta echo foxtrot golf hotel india juliet"))

	// Agreement 8, minus hold-k 2, leaves six unpunctuated words: nothing cuts.
	if got := c.commit(partial("alfa bravo charlie delta echo foxtrot golf hotel XX YY")); got != nil {
		t.Fatalf("revised tail committed %v, want nothing", got)
	}
}

func TestCommitFlushHoldReleasesStalledPrefix(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci"))
	c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci"))

	// maxClause released eight; the stall flushes the two words hold-k withheld.
	got := c.flushHold()
	want := []string{"nove dieci"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flushHold = %v, want %v", got, want)
	}
}

func TestCommitFinalResetsSegment(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	c.commit(final("Prima frase."))

	if got := c.commit(partial("Prima frase.")); got != nil {
		t.Fatalf("post-reset partial committed %v, want nothing until it agrees", got)
	}
}

func TestCommitStablePrefixCommitsWithoutHoldK(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	// A stability-aware adapter commits at punctuation with no agreement wait.
	got := c.commit(Transcript{Text: "Il ponte è aperto, e la via", Stable: true})
	want := []string{"Il ponte è aperto,"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}

	got = c.commit(final("Il ponte è aperto, e la via è libera."))
	want = []string{"e la via è libera."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remainder = %v, want %v", got, want)
	}
}

func TestCommitEmptyTextIsNoop(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)

	if got := c.commit(partial("")); got != nil {
		t.Fatalf("empty partial committed %v, want nothing", got)
	}
	if got := c.commit(final("")); got != nil {
		t.Fatalf("empty final committed %v, want nothing", got)
	}
}

// commitCase drives one transcript stream and asserts the clauses it yields.
type commitCase struct {
	name     string
	updates  []Transcript
	want     [][]string
	fastTurn bool
}

// retokenizationCases replay the re-segmentations whisper produces as context
// grows: spoken content never re-emits and an ambiguous region is skipped.
var retokenizationCases = []commitCase{
	{
		name:     "identical extension",
		fastTurn: true,
		updates: []Transcript{
			partial("va bene, ci vediamo"),
			partial("va bene, ci vediamo domani"),
			partial("va bene, ci vediamo domani, sera"),
			partial("va bene, ci vediamo domani, sera"),
		},
		want: [][]string{nil, {"va bene,"}, nil, {"ci vediamo domani,"}},
	},
	{
		name:     "gonna becomes going to across the final",
		fastTurn: true,
		updates: []Transcript{
			partial("I'm gonna leave now, okay"),
			partial("I'm gonna leave now, okay"),
			final("I'm going to leave now, okay."),
		},
		want: [][]string{nil, {"I'm gonna leave now,"}, {"okay."}},
	},
	{
		name:     "dropped filler in the final",
		fastTurn: true,
		updates: []Transcript{
			partial("so uh let's begin now, friends"),
			partial("so uh let's begin now, friends"),
			final("So let's begin now, friends."),
		},
		want: [][]string{nil, {"so uh let's begin now,"}, {"friends."}},
	},
	{
		name:     "merged words in the final",
		fastTurn: true,
		updates: []Transcript{
			partial("some how it works, right"),
			partial("some how it works, right"),
			final("somehow it works, right."),
		},
		want: [][]string{nil, {"some how it works,"}, {"right."}},
	},
	{
		name:     "final shorter than the last partial",
		fastTurn: true,
		updates: []Transcript{
			partial("uno due tre quattro. cinque sei"),
			partial("uno due tre quattro. cinque sei"),
			final("uno due tre."),
		},
		want: [][]string{nil, {"uno due tre quattro."}, nil},
	},
	{
		name:     "final prepends a word",
		fastTurn: true,
		updates: []Transcript{
			partial("due tre quattro. cinque sei"),
			partial("due tre quattro. cinque sei"),
			final("Uno due tre quattro. cinque sei."),
		},
		want: [][]string{nil, {"due tre quattro."}, {"cinque sei."}},
	},
	{
		name: "stable final only stream stays byte identical",
		updates: []Transcript{
			final("Va bene davvero, ci vediamo."),
			final("uno due tre quattro cinque sei sette otto nove"),
		},
		want: [][]string{
			{"Va bene davvero,", "ci vediamo."},
			{"uno due tre quattro cinque sei sette otto", "nove"},
		},
	},
}

func TestCommitReanchorsAcrossRetokenization(t *testing.T) {
	t.Parallel()

	for _, tc := range retokenizationCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := newCommitter(tc.fastTurn)
			for i, update := range tc.updates {
				if got := c.commit(update); !reflect.DeepEqual(got, tc.want[i]) {
					t.Fatalf("update %d %q: clauses = %v, want %v", i, update.Text, got, tc.want[i])
				}
			}
		})
	}
}

// TestCommitBroadcastHoldsDeeperTailThanCall: nine identical unpunctuated
// words leave the broadcast stable end at 7 (holdTail 2), one short of a
// maxClause cut, where the call policy (holdTail 1) would already release.
func TestCommitBroadcastHoldsDeeperTailThanCall(t *testing.T) {
	t.Parallel()

	c := newCommitter(false)
	nineWords := "uno due tre quattro cinque sei sette otto nove"

	if got := c.commit(partial(nineWords)); got != nil {
		t.Fatalf("first partial committed %v, want nothing", got)
	}
	if got := c.commit(partial(nineWords)); got != nil {
		t.Fatalf("broadcast hold released %v; only the call policy may commit here", got)
	}

	got := c.commit(final(nineWords + "."))
	want := []string{"uno due tre quattro cinque sei sette otto", "nove."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final = %v, want the held words to commit as %v", got, want)
	}
}

func TestIsClauseEnd(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"domani.":  true,
		"bene,":    true,
		"davvero":  false,
		`aperto."`: true,
		"ponte":    false,
		"sì?":      true,
	}
	for word, want := range cases {
		if got := isClauseEnd(word); got != want {
			t.Fatalf("isClauseEnd(%q) = %v, want %v", word, got, want)
		}
	}
}
