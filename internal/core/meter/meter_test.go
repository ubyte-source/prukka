package meter_test

import (
	"math"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/meter"
)

// clock is a controllable minute-resolution test clock.
type clock struct {
	minute int64
}

func (c *clock) now() time.Time {
	return time.Unix(c.minute*60, 0)
}

// close enough for float accumulation.
func approx(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestBookRatesOverSlidingHour(t *testing.T) {
	t.Parallel()

	c := &clock{}
	b := meter.NewBook(c.now)

	b.Add("demo", "stt", 30, 0.5)

	c.minute = 30
	b.Add("demo", "mt", 1000, 0.5)

	if got := b.TotalRate(); !approx(got, 1.0) {
		t.Fatalf("TotalRate at t+30m = %v, want 1.0", got)
	}

	// 61 minutes after the first entry only the second remains in window.
	c.minute = 61
	if got := b.SessionRate("demo"); !approx(got, 0.5) {
		t.Fatalf("SessionRate at t+61m = %v, want 0.5", got)
	}

	c.minute = 200
	if got := b.TotalRate(); !approx(got, 0) {
		t.Fatalf("TotalRate long after = %v, want 0", got)
	}

	if got := b.SessionTotal("demo"); !approx(got, 1.0) {
		t.Fatalf("SessionTotal = %v, want cumulative 1.0", got)
	}
}

func TestBookBreakdownAndForget(t *testing.T) {
	t.Parallel()

	c := &clock{}
	b := meter.NewBook(c.now)

	b.Add("demo", "stt", 30, 0.2)
	b.Add("demo", "stt", 30, 0.2)
	b.Add("demo", "tts", 500, 0.1)

	byKind := b.SessionBreakdown("demo")
	if !approx(byKind["stt"], 0.4) || !approx(byKind["tts"], 0.1) {
		t.Fatalf("SessionBreakdown = %v, want stt 0.4 / tts 0.1", byKind)
	}

	byKind["stt"] = 99
	if again := b.SessionBreakdown("demo"); !approx(again["stt"], 0.4) {
		t.Fatal("SessionBreakdown returned shared internal state")
	}

	b.Forget("demo")

	if got := b.SessionTotal("demo"); !approx(got, 0) {
		t.Fatalf("SessionTotal after Forget = %v, want 0", got)
	}

	if got := b.SessionRate("ghost"); !approx(got, 0) {
		t.Fatalf("SessionRate for unknown session = %v, want 0", got)
	}
}
