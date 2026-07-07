package pipeline_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// tone fills n samples with a constant marker value.
func tone(marker int16, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = marker
	}

	return out
}

func TestTrackPlacesGapsAsSilence(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// One second of speech at 8 s, then one at 10 s: the gap must be
	// silence, timed on the source clock.
	track.Append(8*time.Second, tone(1, pipeline.SampleRate))
	track.Append(10*time.Second, tone(2, pipeline.SampleRate))

	start, ok := track.Start()
	if !ok || start != 8*time.Second {
		t.Fatalf("Start = %v (ok=%v), want the first segment's schedule", start, ok)
	}

	got := make([]int16, 3*pipeline.SampleRate)
	track.Window(start, got)

	if got[0] != 1 || got[pipeline.SampleRate] != 0 || got[2*pipeline.SampleRate] != 2 {
		t.Fatalf("layout wrong: [0]=%d [1s]=%d [2s]=%d, want 1,0,2",
			got[0], got[pipeline.SampleRate], got[2*pipeline.SampleRate])
	}
}

// TestTrackTrimsToLiveWindow: memory bounded, origin slides, recent audio
// survives, ancient reads silence.
func TestTrackTrimsToLiveWindow(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// Ten minutes of continuous audio, appended second by second.
	for s := range 600 {
		track.Append(time.Duration(s)*time.Second, tone(int16(s%100+1), pipeline.SampleRate))
	}

	start, ok := track.Start()
	if !ok {
		t.Fatal("track lost its anchor")
	}

	if start < 9*time.Minute {
		t.Fatalf("Start = %v; ten minutes of live audio were retained (unbounded growth)", start)
	}

	// The newest second must still be intact at its own instant.
	got := make([]int16, pipeline.SampleRate)
	track.Window(599*time.Second, got)

	if got[0] != int16(599%100+1) {
		t.Fatalf("recent audio lost by trim: got %d", got[0])
	}

	// Trimmed history reads as silence, not garbage.
	track.Window(0, got)

	if got[0] != 0 {
		t.Fatalf("trimmed span reads %d, want silence", got[0])
	}
}

func TestTrackSpillsInsteadOfOverwriting(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()

	// Two seconds placed at 0; the next segment is scheduled at 1 s but
	// must spill to 2 s (never overwrite placed speech).
	track.Append(0, tone(1, 2*pipeline.SampleRate))

	placed := track.Append(time.Second, tone(2, pipeline.SampleRate))
	if placed != 2*time.Second {
		t.Fatalf("placed at %v, want the spill to 2s", placed)
	}

	got := make([]int16, 3*pipeline.SampleRate)
	track.Window(0, got)

	if got[2*pipeline.SampleRate] != 2 {
		t.Fatal("spilled segment did not land after the first")
	}
}

// fixedTTS returns a constant one-second 24 kHz take.
type fixedTTS struct{ calls int }

// Speak implements core.TTS.
func (f *fixedTTS) Speak(context.Context, string, core.Lang, core.Voice) (core.PCM, error) {
	f.calls++

	return core.PCM{Data: tone(7, 24000), Rate: 24000, Ch: 1}, nil
}

// halfShaper pretends to resample by returning reference-rate audio of the
// tempo-adjusted length, recording every tempo and pitch it was asked for.
type halfShaper struct {
	tempos  []float64
	pitches []float64
}

// Shape implements pipeline.Shaper.
func (h *halfShaper) Shape(_ context.Context, audio core.PCM, tempo, pitch float64) (core.PCM, error) {
	h.tempos = append(h.tempos, tempo)
	h.pitches = append(h.pitches, pitch)

	n := int(float64(len(audio.Data)) * float64(pipeline.SampleRate) / float64(audio.Rate) / tempo)

	return core.PCM{Data: tone(9, n), Rate: pipeline.SampleRate, Ch: 1}, nil
}

func TestLaneDubsOntoTheTrack(t *testing.T) {
	t.Parallel()

	track := pipeline.NewTrack()
	tts := &fixedTTS{}
	shaper := &halfShaper{}

	it := &captureSink{}
	en := &captureSink{}

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: 8 * time.Second},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output: pipeline.Output{
			Sinks: map[core.Lang]pipeline.Sink{"it": it, "en": en},
			Dub: &pipeline.Dub{
				TTS:    tts,
				Shaper: shaper,
				Voices: map[string]core.Voice{"main": {ID: "nova"}},
				Tracks: map[core.Lang]*pipeline.Track{"en": track},
			},
		},
	}, slog.New(slog.DiscardHandler))

	if err := lane.Run(t.Context(), speechThenSilence(1)); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Only the dubbed target speaks: it-captions stay caption-only.
	if tts.calls != 1 {
		t.Fatalf("TTS calls = %d, want 1 (en only)", tts.calls)
	}

	if len(shaper.tempos) != 1 {
		t.Fatalf("Shape calls = %d, want 1", len(shaper.tempos))
	}

	// Register matching is off: the take keeps the preset's own pitch.
	if shaper.pitches[0] != 1 {
		t.Fatalf("pitch = %v without AdaptPitch, want 1", shaper.pitches[0])
	}

	start, ok := track.Start()
	if !ok || start != 8*time.Second {
		t.Fatalf("track start = %v (ok=%v), want dubbed audio at source PTS 0 + delay 8s", start, ok)
	}

	got := make([]int16, pipeline.SampleRate)
	track.Window(start, got)

	if got[0] != 9 {
		t.Fatal("track holds no shaped audio")
	}

	if len(it.segs) != 1 || len(en.segs) != 1 {
		t.Fatalf("captions it=%d en=%d, want 1 each alongside the dub", len(it.segs), len(en.segs))
	}
}
