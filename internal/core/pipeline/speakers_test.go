package pipeline_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// bank is a six-voice test bank ordered deep → bright.
func bank() []core.Voice {
	return []core.Voice{
		{ID: "v0"}, {ID: "v1"}, {ID: "v2"}, {ID: "v3"}, {ID: "v4"}, {ID: "v5"},
	}
}

// voiceOf returns just the assigned voice, dropping the speaker index.
func voiceOf(s *pipeline.Speakers, f0 float64) core.Voice {
	_, v := s.Classify(f0, bank())

	return v
}

func TestClassifyKeepsOneSpeakerStable(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	// One speaker's natural pitch wobble must map to one index and voice.
	idx, first := s.Classify(118, bank())

	for _, f0 := range []float64{112, 121, 116, 124, 110} {
		gotIdx, got := s.Classify(f0, bank())
		if got.ID != first.ID || gotIdx != idx {
			t.Fatalf("speaker drifted from (%d,%v) to (%d,%v) at %.0f Hz", idx, first, gotIdx, got, f0)
		}
	}
}

// TestCenterF0TracksTheCluster: the EMA center sits between observations;
// unknown indexes report 0.
func TestCenterF0TracksTheCluster(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	idx, _ := s.Classify(120, bank())
	s.Classify(130, bank())

	if got := s.CenterF0(idx); got <= 120 || got >= 130 {
		t.Fatalf("CenterF0 = %.1f, want between the 120 and 130 observations", got)
	}

	if got := s.CenterF0(idx + 7); got != 0 {
		t.Fatalf("unknown speaker CenterF0 = %.1f, want 0", got)
	}

	if got := s.CenterF0(-1); got != 0 {
		t.Fatalf("negative speaker CenterF0 = %.1f, want 0", got)
	}
}

func TestClassifySeparatesTwoSpeakersWithMatchedRegisters(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	deepIdx, deep := s.Classify(110, bank())
	brightIdx, bright := s.Classify(240, bank())

	if deep.ID == bright.ID || deepIdx == brightIdx {
		t.Fatalf("two registers share one speaker: (%d,%v)", deepIdx, deep)
	}

	// Register matching: the deep speaker sits earlier in the bank.
	if deep.ID >= bright.ID {
		t.Fatalf("registers inverted: deep=%v bright=%v", deep, bright)
	}

	// Alternating utterances stay stable in index and voice.
	if voiceOf(s, 115).ID != deep.ID || voiceOf(s, 232).ID != bright.ID {
		t.Fatal("alternating speakers lost their voices")
	}
}

func TestClassifyUnvoicedSticksWithTheLatestSpeaker(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	idx, current := s.Classify(180, bank())

	gotIdx, got := s.Classify(0, bank())
	if got.ID != current.ID || gotIdx != idx {
		t.Fatalf("unvoiced utterance switched speaker: (%d,%v) → (%d,%v)", idx, current, gotIdx, got)
	}
}

func TestClassifyNearbySpeakersGetDistinctVoices(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	// Two speakers in the same register bucket must not share a voice.
	a := voiceOf(s, 150)
	b := voiceOf(s, 185)

	if a.ID == b.ID {
		t.Fatalf("nearby speakers share voice %v", a)
	}
}

// TestClassifyIndexesEvenWithoutABank: subtitle coloring needs the speaker
// index even when no voice bank is configured (captions-only, dubbing off).
func TestClassifyIndexesEvenWithoutABank(t *testing.T) {
	t.Parallel()

	s := pipeline.NewSpeakers()

	deep, deepVoice := s.Classify(110, nil)
	bright, brightVoice := s.Classify(240, nil)

	if deepVoice.ID != "" || brightVoice.ID != "" {
		t.Fatalf("empty bank produced voices: %v %v", deepVoice, brightVoice)
	}

	if deep == bright {
		t.Fatalf("distinct speakers collapsed to index %d without a bank", deep)
	}

	// The index stays stable for the same speaker.
	if again, _ := s.Classify(114, nil); again != deep {
		t.Fatalf("deep speaker index drifted %d → %d", deep, again)
	}
}
