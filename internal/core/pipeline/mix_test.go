package pipeline_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestMixerWaitsForTheBedAnchor(t *testing.T) {
	t.Parallel()

	m := pipeline.NewMixer(pipeline.NewTrack(), pipeline.NewTrack(), -15)

	if _, ok := m.Pull(160); ok {
		t.Fatal("Pull produced audio before the bed was anchored")
	}
}

func TestMixerDucksBedUnderVoice(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	voice := pipeline.NewTrack()

	// Three seconds of loud bed; one second of voice in the middle.
	bed.Append(8*time.Second, tone(20000, 3*pipeline.SampleRate))
	voice.Append(9*time.Second, tone(12000, pipeline.SampleRate))

	m := pipeline.NewMixer(bed, voice, -15)

	out, ok := m.Pull(3 * pipeline.SampleRate)
	if !ok || out.PTS != 8*time.Second {
		t.Fatalf("Pull PTS = %v (ok=%v), want the bed anchor 8s", out.PTS, ok)
	}

	// Well before the voice: bed at full level.
	if early := out.Data[pipeline.SampleRate/2]; early < 19000 {
		t.Fatalf("bed before voice = %d, want ≈ full 20000", early)
	}

	// Deep inside the voiced second (well past the 50 ms attack): the bed
	// sits at −15 dB (≈3557) under the 12000 voice → ≈15557.
	mid := out.Data[pipeline.SampleRate+pipeline.SampleRate/2]
	if mid < 15000 || mid > 16200 {
		t.Fatalf("voiced mix = %d, want ≈15557 (ducked bed + voice)", mid)
	}

	// Well after the voice (past the 300 ms release): bed back to full.
	if late := out.Data[3*pipeline.SampleRate-100]; late < 19000 {
		t.Fatalf("bed after release = %d, want ≈ full 20000", late)
	}
}

func TestMixerZeroFillsPastTheLiveEdge(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	bed.Append(0, tone(1000, pipeline.SampleRate/10))

	m := pipeline.NewMixer(bed, pipeline.NewTrack(), -15)

	out, ok := m.Pull(pipeline.SampleRate / 5)
	if !ok {
		t.Fatal("Pull returned nothing")
	}

	if out.Data[0] != 1000 {
		t.Fatalf("first sample = %d, want the bed", out.Data[0])
	}

	if out.Data[len(out.Data)-1] != 0 {
		t.Fatal("live edge not zero-filled")
	}

	// The clock advances monotonically across pulls.
	second, _ := m.Pull(pipeline.SampleRate / 10)
	if second.PTS != out.PTS+200*time.Millisecond {
		t.Fatalf("second PTS = %v, want %v", second.PTS, out.PTS+200*time.Millisecond)
	}
}

// newBedLane builds a dubbing lane whose only job is filling the bed.
func newBedLane(t *testing.T, bed *pipeline.Track) *pipeline.Captions {
	t.Helper()

	return pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: 8 * time.Second},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output: pipeline.Output{
			Sinks: map[core.Lang]pipeline.Sink{"en": &captureSink{}},
			Dub: &pipeline.Dub{
				TTS:    &fixedTTS{},
				Shaper: &halfShaper{},
				Tracks: map[core.Lang]*pipeline.Track{"en": pipeline.NewTrack()},
				Bed:    bed,
			},
		},
	}, slog.New(slog.DiscardHandler))
}

func TestLaneFeedsTheBed(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()

	lane := newBedLane(t, bed)

	if err := lane.Run(t.Context(), speechThenSilence(1)); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	start, ok := bed.Start()
	if !ok || start != 8*time.Second {
		t.Fatalf("bed anchor = %v (ok=%v), want source PTS 0 + delay 8s", start, ok)
	}

	// The speech second (source 0–1 s) sits at 8–9 s on the delayed clock.
	probe := make([]int16, pipeline.SampleRate)
	bed.Window(8*time.Second, probe)

	if probe[0] == 0 {
		t.Fatal("bed missing the speech audio on the delayed clock")
	}
}
