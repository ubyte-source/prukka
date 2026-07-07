package observability_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/observability"
)

func TestFallbackStateTracksOpenBreakers(t *testing.T) {
	t.Parallel()

	m := observability.NewMetrics()
	f := observability.NewFallbackState(m)

	// Match the metric value line exactly, not the HELP text which also
	// mentions the string "prukka_fallback_active 1".
	active := func() bool {
		for _, line := range strings.Split(scrape(t, m), "\n") {
			if line == "prukka_fallback_active 1" {
				return true
			}
		}

		return false
	}

	if active() {
		t.Fatal("fallback active before any breaker opened")
	}

	// Two breakers open across sessions: the daemon is in fallback.
	f.Observe(true)
	f.Observe(true)

	if !active() {
		t.Fatal("fallback not active with two breakers open")
	}

	// One recovers: still in fallback while the other stays open.
	f.Observe(false)
	if !active() {
		t.Fatal("fallback cleared too early with one breaker still open")
	}

	// The last recovers: fallback clears.
	f.Observe(false)
	if active() {
		t.Fatal("fallback still active after every breaker recovered")
	}
}
