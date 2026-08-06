package session_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
)

func TestApplyFlagsParsesTheWireVocabulary(t *testing.T) {
	t.Parallel()

	s := demo("demo")
	if err := s.ApplyFlags(map[string]string{
		session.FlagSubs:     "BURN",
		session.FlagDub:      "off",
		session.FlagBed:      "-12dB",
		session.FlagSource:   "IT",
		session.FlagPair:     "demo-out",
		session.FlagDubLangs: "it",
	}); err != nil {
		t.Fatalf("ApplyFlags returned error: %v", err)
	}

	if s.Subs != session.SubsBurn || s.Dub != session.DubOff || s.Bed != "-12dB" {
		t.Fatalf("modes = (%q, %q, %q), want (burn, off, -12dB)", s.Subs, s.Dub, s.Bed)
	}
	if s.SourceLang != "it" || s.Pair != "demo-out" {
		t.Fatalf("source/pair = (%q, %q), want (it, demo-out)", s.SourceLang, s.Pair)
	}
	if !s.DubLangs.Chosen() || len(s.DubLangs.Langs()) != 1 || s.DubLangs.Langs()[0] != "it" {
		t.Fatalf("dub selection = %v (chosen %v), want [it]", s.DubLangs.Langs(), s.DubLangs.Chosen())
	}
}

func TestApplyFlagsRefusesOptionsNoLaneReads(t *testing.T) {
	t.Parallel()

	for _, flags := range []map[string]string{
		{"voices": "manual"},
		{"subtitles": "vtt"},
		{session.FlagSubs: "srt"},
		{session.FlagDub: "loud"},
		{session.FlagBed: "loud"},
		{session.FlagSource: "klingon"},
		{session.FlagPair: "Meeting Out"},
		{session.FlagDubLangs: "klingon"},
	} {
		s := demo("demo")
		if err := s.ApplyFlags(flags); !errors.Is(err, session.ErrInvalidFlags) {
			t.Errorf("ApplyFlags(%v) error = %v, want ErrInvalidFlags", flags, err)
		}
	}
}

func TestFlagsWireEchoesOnlyWhatTheSessionChose(t *testing.T) {
	t.Parallel()

	bare := demo("demo")
	if wire := bare.FlagsWire(); len(wire) != 0 {
		t.Fatalf("bare session wire flags = %v, want none", wire)
	}

	chosen := map[string]string{
		session.FlagSubs:     "vtt",
		session.FlagDub:      "off",
		session.FlagBed:      "off",
		session.FlagSource:   "it",
		session.FlagPair:     "demo-out",
		session.FlagDubLangs: "",
	}
	s := demo("demo")
	if err := s.ApplyFlags(chosen); err != nil {
		t.Fatalf("ApplyFlags returned error: %v", err)
	}

	// dub_langs is the one option whose empty value carries meaning.
	want := maps.Clone(chosen)
	if got := s.FlagsWire(); !maps.Equal(got, want) {
		t.Fatalf("wire flags = %v, want %v", got, want)
	}
}

func TestDubbedLangsResolvesTheTriState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*session.Session)
		want   []core.Lang
	}{
		{
			name:   "absent dubs every target",
			mutate: func(*session.Session) {},
			want:   []core.Lang{"it", "en"},
		},
		{
			name:   "empty selection is captions only",
			mutate: func(s *session.Session) { s.DubLangs = session.DubOnly() },
			want:   []core.Lang{},
		},
		{
			name:   "subset keeps target order",
			mutate: func(s *session.Session) { s.DubLangs = session.DubOnly("en", "it") },
			want:   []core.Lang{"it", "en"},
		},
		{
			name:   "dub off overrides the selection",
			mutate: func(s *session.Session) { s.Dub = session.DubOff },
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := demo("demo")
			tc.mutate(s)

			if got := s.DubbedLangs(); len(got) != len(tc.want) ||
				(len(got) > 0 && (got[0] != tc.want[0] || got[len(got)-1] != tc.want[len(tc.want)-1])) {
				t.Fatalf("DubbedLangs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSubsAndDubAndBed(t *testing.T) {
	t.Parallel()

	if mode, err := session.ParseSubs(" Vtt "); err != nil || mode != session.SubsVTT {
		t.Fatalf("ParseSubs = (%q, %v), want vtt", mode, err)
	}
	if _, err := session.ParseSubs("srt"); !errors.Is(err, session.ErrInvalidFlags) {
		t.Fatalf("ParseSubs(srt) error = %v, want ErrInvalidFlags", err)
	}
	if mode, err := session.ParseDub("off"); err != nil || mode != session.DubOff {
		t.Fatalf("ParseDub = (%q, %v), want off", mode, err)
	}
	if mode, err := session.ParseBed("-12dB"); err != nil || mode != "-12dB" {
		t.Fatalf("ParseBed = (%q, %v), want -12dB", mode, err)
	}
	if _, err := session.ParseBed("+3dB"); !errors.Is(err, session.ErrInvalidFlags) {
		t.Fatalf("ParseBed(+3dB) error = %v, want ErrInvalidFlags", err)
	}
}
