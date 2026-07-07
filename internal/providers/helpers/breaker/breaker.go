// Package breaker adds a per-model circuit breaker around the AI ports;
// it probes and recovers automatically.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Policy tunes one breaker.
type Policy struct {
	// Now is the clock; nil selects time.Now (injected for tests).
	Now func() time.Time
	// Cooldown is how long the breaker stays open before allowing one probe.
	Cooldown time.Duration
	// Threshold is the number of consecutive transient failures that trips
	// the breaker open.
	Threshold int
}

// Default returns a breaker that trips after two transient failures and
// probes again after ten seconds.
func Default() Policy {
	return Policy{Threshold: 2, Cooldown: 10 * time.Second}
}

// Observer is notified when a breaker opens (true) or closes (false), so the
// daemon can publish the fallback state (prukka_fallback_active).
type Observer func(open bool)

// ErrOpen is returned while the breaker is open. It wraps ErrTransient so
// callers already handling transient failures fall back correctly.
var ErrOpen = fmt.Errorf("%w: circuit breaker open", core.ErrTransient)

// breaker is the shared state machine behind the stage decorators.
type breaker struct {
	now      func() time.Time
	observe  Observer
	openTill time.Time
	cooldown time.Duration
	mu       sync.Mutex
	failures int
	limit    int
	open     bool
}

// newBreaker builds the state machine from a policy.
func newBreaker(p Policy, observe Observer) *breaker {
	now := p.Now
	if now == nil {
		now = time.Now
	}

	if observe == nil {
		observe = func(bool) {}
	}

	return &breaker{now: now, observe: observe, cooldown: p.Cooldown, limit: p.Threshold}
}

// allow reports whether a call may proceed. While open it permits exactly
// one probe once the cooldown elapses.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.open {
		return true
	}

	return !b.now().Before(b.openTill)
}

// record folds one call result into the state, tripping or clearing the
// breaker and notifying the observer on transitions.
func (b *breaker) record(err error) {
	transient := errors.Is(err, core.ErrTransient)

	b.mu.Lock()

	var toClose, toOpen bool

	switch {
	case !transient:
		// Success or a caller-side error (bad request) proves the provider
		// is reachable: reset.
		toClose = b.open
		b.open = false
		b.failures = 0
	default:
		b.failures++
		if b.failures >= b.limit && !b.open {
			b.open = true
			b.openTill = b.now().Add(b.cooldown)
			toOpen = true
		} else if b.open {
			// A failed probe re-arms the cooldown.
			b.openTill = b.now().Add(b.cooldown)
		}
	}

	b.mu.Unlock()

	if toOpen {
		b.observe(true)
	}

	if toClose {
		b.observe(false)
	}
}

// STT wraps next with a circuit breaker.
func STT(next core.STT, p Policy, observe Observer) core.STT {
	return &sttBreaker{next: next, b: newBreaker(p, observe)}
}

// MT wraps next with a circuit breaker.
func MT(next core.MT, p Policy, observe Observer) core.MT {
	return &mtBreaker{next: next, b: newBreaker(p, observe)}
}

// TTS wraps next with a circuit breaker.
func TTS(next core.TTS, p Policy, observe Observer) core.TTS {
	return &ttsBreaker{next: next, b: newBreaker(p, observe)}
}

type sttBreaker struct {
	next core.STT
	b    *breaker
}

// Transcribe implements core.STT.
func (s *sttBreaker) Transcribe(ctx context.Context, u *core.Utterance, hint core.Lang) (core.Transcript, error) {
	if !s.b.allow() {
		return core.Transcript{}, ErrOpen
	}

	out, err := s.next.Transcribe(ctx, u, hint)
	s.b.record(err)

	return out, err
}

type mtBreaker struct {
	next core.MT
	b    *breaker
}

// Translate implements core.MT.
func (m *mtBreaker) Translate(ctx context.Context, t core.Transcript, to core.Lang, o core.MTOpts) (string, error) {
	if !m.b.allow() {
		return "", ErrOpen
	}

	out, err := m.next.Translate(ctx, t, to, o)
	m.b.record(err)

	return out, err
}

type ttsBreaker struct {
	next core.TTS
	b    *breaker
}

// Speak implements core.TTS.
func (t *ttsBreaker) Speak(ctx context.Context, text string, to core.Lang, v core.Voice) (core.PCM, error) {
	if !t.b.allow() {
		return core.PCM{}, ErrOpen
	}

	out, err := t.next.Speak(ctx, text, to, v)
	t.b.record(err)

	return out, err
}
