package main

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
)

func TestBedLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		flag string
		want float64
	}{
		{flag: "-15dB", want: -15},
		{flag: "-9dB", want: -9},
		{flag: "-20", want: -20},
		{flag: "  -12dB ", want: -12},
		{flag: "", want: -15},
		{flag: "off", want: -15},
	}

	for _, tc := range cases {
		if got := bedLevel(tc.flag); got != tc.want {
			t.Errorf("bedLevel(%q) = %v, want %v", tc.flag, got, tc.want)
		}
	}
}

func TestSourceHint(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		flags map[string]string
		want  core.Lang
	}{
		{name: "absent", flags: nil, want: core.LangAuto},
		{name: "valid", flags: map[string]string{"source": "it"}, want: "it"},
		{name: "region", flags: map[string]string{"source": "de-CH"}, want: "de-CH"},
		{name: "invalid falls back to auto", flags: map[string]string{"source": "nope"}, want: core.LangAuto},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := &session.Session{Flags: tc.flags}
			if got := sourceHint(s); got != tc.want {
				t.Fatalf("sourceHint = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBackendSelection: the local backend wires all three ports and a
// preset bank, exactly like the hosted one.
func TestBackendSelection(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Backend = config.BackendLocal

	b, err := newBackend(cfg, meterFunc(func(string, string, float64, float64) {}))
	if err != nil {
		t.Fatalf("local backend: %v", err)
	}

	if len(b.Bank()) == 0 {
		t.Fatal("local backend carries no preset voice bank")
	}

	stt, mt, tts := b.ForSession("demo")
	if stt == nil || mt == nil || tts == nil {
		t.Fatal("local backend returned nil ports")
	}
}

// TestCloneSelection: cloning defaults off; Cartesia with a resolvable key
// yields a voice port.
func TestCloneSelection(t *testing.T) {
	t.Parallel()

	off, enabled, err := newCloneTTS(config.Default())
	if err != nil {
		t.Fatalf("clone off: %v", err)
	}

	if off != nil || enabled {
		t.Fatal("cloning is on by default")
	}

	cfg := config.Default()
	cfg.Providers.Clone = config.CloneCartesia
	cfg.Providers.Cartesia.Key = "cartesia-test-key"

	clone, enabled, err := newCloneTTS(cfg)
	if err != nil {
		t.Fatalf("cartesia clone: %v", err)
	}

	if clone == nil || !enabled {
		t.Fatal("cartesia clone port is nil")
	}

	// Pitch adaptation is in-engine: no cloning port and no key required.
	pitch := config.Default()
	pitch.Providers.Clone = config.ClonePitch

	adapted, enabled, err := newCloneTTS(pitch)
	if err != nil || adapted != nil || enabled {
		t.Fatalf("pitch mode = (%v, %v, %v), want no clone port and no error", adapted, enabled, err)
	}
}

// meterFunc adapts a function to core.Meter.
type meterFunc func(session, kind string, units, eur float64)

func (m meterFunc) Add(slug, kind string, units, eur float64) { m(slug, kind, units, eur) }
