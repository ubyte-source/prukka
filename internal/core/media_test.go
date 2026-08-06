package core_test

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestVoiceSupportsLanguageFamily(t *testing.T) {
	t.Parallel()

	english := core.Voice{ID: "lessac", Lang: "en-US"}
	if !english.Supports("en-GB") {
		t.Fatal("English voice rejected an English regional target")
	}
	if english.Supports("it") {
		t.Fatal("English voice accepted an Italian target")
	}
	if !(core.Voice{ID: "multilingual"}).Supports("it") {
		t.Fatal("multilingual voice rejected a target")
	}
}

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

	for _, raw := range []string{"off", "OFF", " off "} {
		got, err := core.BedLevel(raw)
		if err != nil || !math.IsInf(got, -1) {
			t.Fatalf("BedLevel(%q) = (%v, %v), want -Inf", raw, got, err)
		}
	}
}

func TestErrNotReadySurvivesWrapping(t *testing.T) {
	t.Parallel()

	if err := fmt.Errorf("start output: %w", core.ErrNotReady); !errors.Is(err, core.ErrNotReady) {
		t.Fatal("wrapped ErrNotReady no longer matches errors.Is")
	}
}
