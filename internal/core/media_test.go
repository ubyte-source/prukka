package core_test

import (
	"math"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestBedLevelParsesOnlyTheSupportedRange(t *testing.T) {
	t.Parallel()

	valid := []struct {
		raw  string
		want float64
	}{
		{raw: "-15dB", want: -15},
		{raw: " -9 ", want: -9},
		{raw: "0dB", want: 0},
		{raw: "-60dB", want: -60},
	}
	for _, test := range valid {
		raw, want := test.raw, test.want
		got, err := core.BedLevel(raw)
		if err != nil || got != want {
			t.Fatalf("BedLevel(%q) = (%v, %v), want %v", raw, got, err, want)
		}
	}

	for _, raw := range []string{"", "loud", "NaN", "+Inf", "-Inf", "-61dB", "1dB"} {
		if _, err := core.BedLevel(raw); err == nil {
			t.Fatalf("BedLevel accepted %q", raw)
		}
	}

	// "off" mutes the bed entirely (calls): −Inf, distinct from any dB duck.
	for _, raw := range []string{"off", "OFF", " off "} {
		got, err := core.BedLevel(raw)
		if err != nil || !math.IsInf(got, -1) {
			t.Fatalf("BedLevel(%q) = (%v, %v), want -Inf", raw, got, err)
		}
	}
}
