package engine

import (
	"reflect"
	"testing"
)

// partial is a plain, revisable transcript update.
func partial(text string) Transcript { return Transcript{Text: text} }

// final closes a segment with its settled text.
func final(text string) Transcript { return Transcript{Text: text, Stable: true, Final: true} }

func TestCommitFinalCutsAndFlushes(t *testing.T) {
	t.Parallel()

	var c committer

	got := c.commit(final("Va bene davvero, ci vediamo."))
	want := []string{"Va bene davvero,", "ci vediamo."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitFinalHoldsTinyLeadingClause(t *testing.T) {
	t.Parallel()

	var c committer

	// The comma after two words is below minClause, so the whole line commits
	// as one clause rather than splitting off "Va bene,".
	got := c.commit(final("Va bene, ci vediamo domani."))
	want := []string{"Va bene, ci vediamo domani."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitForcesClauseAtMaxWords(t *testing.T) {
	t.Parallel()

	var c committer

	got := c.commit(final("uno due tre quattro cinque sei sette otto nove"))
	want := []string{"uno due tre quattro cinque sei sette otto", "nove"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
}

func TestCommitLocalAgreementCommitsStablePrefix(t *testing.T) {
	t.Parallel()

	var c committer

	// First sighting agrees with nothing, so nothing commits.
	if got := c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci")); got != nil {
		t.Fatalf("first partial committed %v, want nothing", got)
	}

	// The same ten words now agree; the hold-k tail withholds the last two, so
	// the first maxClause words commit and "nove dieci" stay pending.
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

	var c committer

	c.commit(partial("alfa bravo charlie delta echo foxtrot golf hotel india juliet"))

	// The last two words are revised: local agreement holds at eight, hold-k
	// keeps back two more, so only the first six-word run is eligible — and
	// with no punctuation and under maxClause, nothing yet commits.
	if got := c.commit(partial("alfa bravo charlie delta echo foxtrot golf hotel XX YY")); got != nil {
		t.Fatalf("revised tail committed %v, want nothing", got)
	}
}

func TestCommitFlushHoldReleasesStalledPrefix(t *testing.T) {
	t.Parallel()

	var c committer

	c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci"))
	c.commit(partial("uno due tre quattro cinque sei sette otto nove dieci"))

	// maxClause already released the first eight words; a stall flushes the two
	// agreed words the hold-k tail was withholding.
	got := c.flushHold()
	want := []string{"nove dieci"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flushHold = %v, want %v", got, want)
	}
}

func TestCommitFinalResetsSegment(t *testing.T) {
	t.Parallel()

	var c committer

	c.commit(final("Prima frase."))

	// A fresh segment must not agree with the previous one's words.
	if got := c.commit(partial("Prima frase.")); got != nil {
		t.Fatalf("post-reset partial committed %v, want nothing until it agrees", got)
	}
}

func TestCommitStablePrefixCommitsWithoutHoldK(t *testing.T) {
	t.Parallel()

	var c committer

	// A stability-aware adapter reports a committed prefix directly; it commits
	// at punctuation with no local-agreement wait, remainder held for the next.
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

	var c committer

	if got := c.commit(partial("")); got != nil {
		t.Fatalf("empty partial committed %v, want nothing", got)
	}
	if got := c.commit(final("")); got != nil {
		t.Fatalf("empty final committed %v, want nothing", got)
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
