// Package meter implements the cost-accounting port core.Meter: providers
// report usage, every surface reads euros-per-hour.
package meter

import (
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// rateBuckets is the sliding-window resolution: one bucket per minute over
// the last hour, so a window sum is directly euros per hour.
const rateBuckets = 60

// Book aggregates provider spend per session and kind. It is safe for
// concurrent use and implements core.Meter.
type Book struct {
	now      func() time.Time
	sessions map[string]*ledger
	mu       sync.Mutex
}

// Compile-time port check.
var _ core.Meter = (*Book)(nil)

// ledger tracks one session's spend.
type ledger struct {
	eurByKind   map[string]float64
	unitsByKind map[string]float64
	buckets     [rateBuckets]float64
	head        int64 // unix minute of the newest bucket
	totalEUR    float64
	primed      bool // head is meaningful; minute 0 is a valid head value
}

// NewBook returns an empty book. The clock is injected for tests;
// nil selects time.Now.
func NewBook(now func() time.Time) *Book {
	if now == nil {
		now = time.Now
	}

	return &Book{now: now, sessions: map[string]*ledger{}}
}

// Add implements core.Meter.
func (b *Book) Add(session, kind string, units, eur float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	l, ok := b.sessions[session]
	if !ok {
		l = &ledger{eurByKind: map[string]float64{}, unitsByKind: map[string]float64{}}
		b.sessions[session] = l
	}

	minute := b.now().Unix() / 60
	l.advance(minute)
	l.buckets[minute%rateBuckets] += eur
	l.totalEUR += eur
	l.eurByKind[kind] += eur
	l.unitsByKind[kind] += units
}

// SessionRate returns a session's spend over the trailing hour — read
// directly as euros per hour.
func (b *Book) SessionRate(session string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	l, ok := b.sessions[session]
	if !ok {
		return 0
	}

	return l.rate(b.now().Unix() / 60)
}

// TotalRate returns the daemon-wide spend rate in euros per hour.
func (b *Book) TotalRate() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	minute := b.now().Unix() / 60
	total := 0.0

	for _, l := range b.sessions {
		total += l.rate(minute)
	}

	return total
}

// SessionTotal returns a session's cumulative spend in euros.
func (b *Book) SessionTotal(session string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	l, ok := b.sessions[session]
	if !ok {
		return 0
	}

	return l.totalEUR
}

// SessionBreakdown returns a session's cumulative spend per kind (stt, mt,
// tts, …) as an owned copy — the dashboard's per-lane cost view reads this.
func (b *Book) SessionBreakdown(session string) map[string]float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	l, ok := b.sessions[session]
	if !ok {
		return map[string]float64{}
	}

	out := make(map[string]float64, len(l.eurByKind))
	for kind, eur := range l.eurByKind {
		out[kind] = eur
	}

	return out
}

// Forget drops a session's ledger once the session is deleted.
func (b *Book) Forget(session string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.sessions, session)
}

// advance zeroes buckets between the ledger head and the current minute.
func (l *ledger) advance(minute int64) {
	switch gap := minute - l.head; {
	case !l.primed || gap >= rateBuckets:
		clear(l.buckets[:])

		l.head = minute
		l.primed = true
	case gap > 0:
		for m := l.head + 1; m <= minute; m++ {
			l.buckets[m%rateBuckets] = 0
		}

		l.head = minute
	}
}

// rate sums the trailing window after advancing to the current minute.
func (l *ledger) rate(minute int64) float64 {
	l.advance(minute)

	sum := 0.0
	for _, v := range l.buckets {
		sum += v
	}

	return sum
}
