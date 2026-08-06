package realtime

import (
	"strings"
	"unicode"
)

// Wait-k commit tuning: a source word reaches the irrevocable audio path only
// after agreeing across two consecutive partials and surviving the hold-k
// tail, then committed source is cut into minClause..maxClause-word clauses.
const (
	holdTail  = 2 // trailing agreed words withheld — they may still revise
	minClause = 3 // do not cut at punctuation below this many words
	maxClause = 8 // force a cut here even without punctuation, to bound latency

	callHoldTail  = 1 // fast-turn still holds a revisable word
	callMinClause = 2 // short punctuated turns can leave immediately
	// Four-word forced cuts destroyed the grammatical context MT needs, for
	// example splitting "this is the incoming caller" before its noun.
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
		holdTail:  callHoldTail,
		minClause: callMinClause,
		maxClause: callMaxClause,
	}
)

// clauseEnds are the trailing runes that close a clause for commit purposes;
// clauseTrailers are closing marks skipped when locating that trailing rune.
const (
	clauseEnds     = ".,;:!?…"
	clauseTrailers = `")]'”’»`
)

// committer turns a stream of transcription partials into irrevocable source
// clauses. It is single-goroutine state owned by the caller.
type committer struct {
	prev    []string // words of the previous partial, for local agreement
	last    []string // words of the most recent partial
	agreed  int      // words of last agreeing with prev, before the hold-k tail
	emitted int      // frontier into last already cut into clauses, re-anchored per decode
	policy  commitPolicy
}

func newCommitter(fastTurn bool) committer {
	policy := broadcastCommitPolicy
	if fastTurn {
		policy = callCommitPolicy
	}

	return committer{policy: policy}
}

// commit folds one transcript update into the segment and returns the clauses
// that became irrevocable; a final also flushes the remainder and resets.
func (c *committer) commit(t Transcript) []string {
	c.observe(strings.Fields(t.Text), t.Stable || t.Final)

	clauses := c.cut(c.stableEnd(t.Stable || t.Final), t.Final)
	if t.Final {
		c.reset()
	}

	return clauses
}

// flushHold commits the local-agreement prefix without waiting for the hold-k
// tail; the lane calls it at end of stream, where a segment can close with no
// Final and lose its last clause.
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

	c.emitted = c.anchor(words)
	c.prev, c.last = words, words
}

// anchorWindow is how many pending words must reappear, in order, for the
// emitted frontier to re-anchor after a decode re-tokenizes spoken words.
const anchorWindow = 2

// anchor relocates the emitted frontier inside a new tokenization: Whisper
// re-segments as context grows ("gonna" becomes "going to"), so progress is
// content, not an index. When alignment is ambiguous it skips rather than
// duplicates, because a duplicate is audible and a skipped region was already
// covered by audio.
func (c *committer) anchor(words []string) int {
	if c.emitted == 0 {
		return 0
	}

	kept := lcp(c.last, words)
	if kept >= c.emitted {
		return c.emitted
	}

	pending := c.last[c.emitted:min(c.emitted+anchorWindow, len(c.last))]
	if len(pending) == 0 {
		return len(words)
	}

	for j := len(words) - len(pending); j >= kept; j-- {
		if lcp(pending, words[j:]) == len(pending) {
			return j
		}
	}

	return len(words)
}

// stableEnd is the exclusive word index up to which the current partial is
// safe to cut: the whole text when stable, else the agreed prefix less hold-k.
func (c *committer) stableEnd(stable bool) int {
	if stable {
		return len(c.last)
	}

	return clamp(c.agreed-c.policy.holdTail, c.emitted, len(c.last))
}

// cut carves clauses from the not-yet-emitted words up to end, advancing the
// emitted mark; force also emits a remainder with no closing boundary.
func (c *committer) cut(end int, force bool) []string {
	var clauses []string
	policy := c.policy

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

func (c *committer) reset() {
	c.prev, c.last, c.agreed, c.emitted = nil, nil, 0, 0
}

// lcp is the length of the longest common prefix of two word slices.
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

// agreementWord ignores the case and edge punctuation Whisper adds as context
// grows, which would otherwise reset local agreement to zero.
func agreementWord(word string) string {
	return strings.ToLower(strings.TrimFunc(word, unicode.IsPunct))
}

// isClauseEnd reports whether a word closes a clause, looking past trailing
// closing quotes and brackets.
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
