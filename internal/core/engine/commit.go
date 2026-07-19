package engine

import (
	"strings"
	"unicode"
)

// Wait-k commit tuning. A source word reaches the irrevocable audio path only
// once it has agreed across two consecutive partials (local-agreement-2) and
// survived the hold-k tail; committed source is then cut into clauses of
// minClause..maxClause words at punctuation, so translation reordering stays
// inside a clause the listener has not yet heard.
const (
	holdTail  = 2 // trailing agreed words withheld — they may still revise
	minClause = 3 // do not cut at punctuation below this many words
	maxClause = 8 // force a cut here even without punctuation, to bound latency

	callHoldTail  = 1 // fast-turn still holds a revisable word
	callMinClause = 2 // short punctuated turns can leave immediately
	// Four-word forced cuts destroyed the grammatical context MT needs (for
	// example, splitting "this is the incoming caller" before its noun). Calls
	// still release short punctuated clauses immediately, while an unpunctuated
	// run keeps one sentence-sized context window before the latency safeguard.
	callMaxClause = 8
)

type commitPolicy struct {
	holdTail  int
	minClause int
	maxClause int
}

var (
	broadcastCommitPolicy = commitPolicy{holdTail: holdTail, minClause: minClause, maxClause: maxClause}
	callCommitPolicy      = commitPolicy{
		holdTail: callHoldTail, minClause: callMinClause, maxClause: callMaxClause,
	}
)

// clauseEnds are the trailing runes that close a clause for commit purposes;
// clauseTrailers are closing marks skipped when locating that trailing rune.
const (
	clauseEnds     = ".,;:!?…"
	clauseTrailers = `")]'”’»`
)

// committer turns a stream of transcription partials into irrevocable source
// clauses — the audio path's wait-k policy. Captions may revise freely, but a
// clause handed out here has been spoken stably and will be translated and
// voiced as a closed unit. It is single-goroutine state owned by the caller.
type committer struct {
	prev    []string // words of the previous partial, for local agreement
	last    []string // words of the most recent partial
	agreed  int      // words of last agreeing with prev, before the hold-k tail
	emitted int      // words of the current segment already cut into clauses
	policy  commitPolicy
}

func newCommitter(fastTurn bool) committer {
	policy := broadcastCommitPolicy
	if fastTurn {
		policy = callCommitPolicy
	}

	return committer{policy: policy}
}

func (c *committer) activePolicy() commitPolicy {
	if c.policy.maxClause == 0 {
		return broadcastCommitPolicy
	}

	return c.policy
}

// commit folds one transcript update into the segment and returns any clauses
// that became irrevocable. A stable or final update commits its whole text; a
// plain partial commits only its local-agreement prefix minus the hold-k tail.
// A final update also flushes the remainder and closes the segment.
func (c *committer) commit(t Transcript) []string {
	c.observe(strings.Fields(t.Text), t.Stable || t.Final)

	clauses := c.cut(c.stableEnd(t.Stable || t.Final), t.Final)
	if t.Final {
		c.reset()
	}

	return clauses
}

// flushHold commits the current local-agreement prefix without waiting for the
// hold-k tail or a clause boundary. The engine calls it at end of stream to
// release a segment that closed without a Final, so its last clause is not
// dropped.
func (c *committer) flushHold() []string {
	return c.cut(min(c.agreed, len(c.last)), true)
}

// observe records one partial and how far it agrees with its predecessor.
func (c *committer) observe(words []string, stable bool) {
	if stable {
		c.agreed = len(words)
	} else {
		c.agreed = lcp(c.prev, words)
	}

	c.prev, c.last = words, words
}

// stableEnd is the exclusive word index up to which the current partial is
// safe to cut: the whole text when stable, else the agreed prefix less hold-k.
func (c *committer) stableEnd(stable bool) int {
	if stable {
		return len(c.last)
	}

	return clamp(c.agreed-c.activePolicy().holdTail, c.emitted, len(c.last))
}

// cut carves clauses from the not-yet-emitted words up to end, advancing the
// emitted mark. force emits a trailing remainder that has no closing boundary,
// used when the segment ends or a stalled prefix is flushed.
func (c *committer) cut(end int, force bool) []string {
	var clauses []string
	policy := c.activePolicy()

	start := c.emitted
	for i := c.emitted; i < end; i++ {
		if n := i - start + 1; (isClauseEnd(c.last[i]) && n >= policy.minClause) || n >= policy.maxClause {
			clauses = append(clauses, strings.Join(c.last[start:i+1], " "))
			start = i + 1
		}
	}

	if force && start < end {
		clauses = append(clauses, strings.Join(c.last[start:end], " "))
		start = end
	}

	c.emitted = start

	return clauses
}

// reset drops all segment state so the next partial starts a fresh segment.
func (c *committer) reset() {
	c.prev, c.last, c.agreed, c.emitted = nil, nil, 0, 0
}

// lcp is the length of the longest position-wise common prefix of two word
// slices — the local-agreement measure between consecutive partials.
func lcp(a, b []string) int {
	n := min(len(a), len(b))

	i := 0
	for i < n && wordsAgree(a[i], b[i]) {
		i++
	}

	return i
}

func wordsAgree(a, b string) bool {
	normalA, normalB := agreementWord(a), agreementWord(b)
	if normalA == "" || normalB == "" {
		return strings.EqualFold(a, b)
	}

	return normalA == normalB
}

// agreementWord ignores surface-only case and edge-punctuation revisions.
// Whisper frequently adds capitalization or punctuation as context grows; the
// spoken token is still stable and must not reset local agreement to zero.
func agreementWord(word string) string {
	return strings.ToLower(strings.TrimFunc(word, unicode.IsPunct))
}

// isClauseEnd reports whether a word closes a clause, looking past any trailing
// closing quotes or brackets to the punctuation beneath.
func isClauseEnd(word string) bool {
	runes := []rune(word)
	for i := len(runes) - 1; i >= 0; i-- {
		if strings.ContainsRune(clauseTrailers, runes[i]) {
			continue
		}

		return strings.ContainsRune(clauseEnds, runes[i])
	}

	return false
}

// clamp bounds v to [lo, hi]; callers pass lo <= hi.
func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
