package pipeline_test

import (
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// frame builds one reference-format frame of constant amplitude at pts.
func frame(amplitude int16, pts time.Duration) core.PCM {
	data := make([]int16, pipeline.FrameSamples)
	for i := range data {
		data[i] = amplitude
	}

	return core.PCM{Data: data, Rate: pipeline.SampleRate, Ch: 1, PTS: pts}
}

// feedPattern feeds speech then silence frames, returning all utterances.
func feedPattern(v core.VAD, speech, silence time.Duration) []core.Utterance {
	var (
		out []core.Utterance
		pts time.Duration
	)

	for pts < speech {
		out = append(out, v.Feed(frame(3000, pts))...)
		pts += pipeline.FrameDuration
	}

	end := pts + silence
	for pts < end {
		out = append(out, v.Feed(frame(0, pts))...)
		pts += pipeline.FrameDuration
	}

	return out
}

func TestEnergyVADEndpointsOnSilence(t *testing.T) {
	t.Parallel()

	v := pipeline.NewEnergyVAD(pipeline.BroadcastVAD())

	got := feedPattern(v, time.Second, time.Second)
	if len(got) != 1 {
		t.Fatalf("emitted %d utterances, want 1", len(got))
	}

	u := got[0]
	if !u.Final {
		t.Fatal("utterance not marked Final")
	}

	if u.Audio.PTS != 0 {
		t.Fatalf("utterance PTS = %v, want 0 (speech onset)", u.Audio.PTS)
	}

	dur := time.Duration(len(u.Audio.Data)) * time.Second / pipeline.SampleRate
	if dur < time.Second || dur > 2*time.Second {
		t.Fatalf("utterance duration = %v, want speech plus bounded trailing silence", dur)
	}
}

func TestEnergyVADHardCut(t *testing.T) {
	t.Parallel()

	cfg := pipeline.BroadcastVAD()
	v := pipeline.NewEnergyVAD(cfg)

	got := feedPattern(v, 9*time.Second, time.Second)
	if len(got) != 2 {
		t.Fatalf("emitted %d utterances, want 2 (hard cut at %v)", len(got), cfg.MaxUtterance)
	}

	first := time.Duration(len(got[0].Audio.Data)) * time.Second / pipeline.SampleRate
	if first != cfg.MaxUtterance {
		t.Fatalf("first utterance = %v, want the %v hard cut", first, cfg.MaxUtterance)
	}

	if got[1].Audio.PTS != cfg.MaxUtterance {
		t.Fatalf("second utterance PTS = %v, want %v (continues after cut)", got[1].Audio.PTS, cfg.MaxUtterance)
	}
}

func TestEnergyVADDropsBlips(t *testing.T) {
	t.Parallel()

	v := pipeline.NewEnergyVAD(pipeline.BroadcastVAD())

	if got := feedPattern(v, 100*time.Millisecond, time.Second); len(got) != 0 {
		t.Fatalf("emitted %d utterances for a 100 ms blip, want 0", len(got))
	}
}

func TestEnergyVADFlushEmitsBufferedSpeech(t *testing.T) {
	t.Parallel()

	v := pipeline.NewEnergyVAD(pipeline.BroadcastVAD())

	// 7 s of continuous speech: no silence endpoint, no hard cut — exactly
	// the endpointing tail-flush behavior.
	for pts := time.Duration(0); pts < 7*time.Second; pts += pipeline.FrameDuration {
		if got := v.Feed(frame(3000, pts)); len(got) != 0 {
			t.Fatalf("unexpected endpoint at %v", pts)
		}
	}

	got := v.Flush()
	if len(got) != 1 || !got[0].Final {
		t.Fatalf("Flush = %d utterances, want the buffered monologue", len(got))
	}

	dur := time.Duration(len(got[0].Audio.Data)) * time.Second / pipeline.SampleRate
	if dur != 7*time.Second {
		t.Fatalf("flushed duration = %v, want 7s", dur)
	}

	if again := v.Flush(); len(again) != 0 {
		t.Fatalf("second Flush = %d utterances, want none", len(again))
	}
}

func TestEnergyVADFlushDropsBlips(t *testing.T) {
	t.Parallel()

	v := pipeline.NewEnergyVAD(pipeline.BroadcastVAD())
	v.Feed(frame(3000, 0))

	if got := v.Flush(); len(got) != 0 {
		t.Fatalf("Flush emitted a %d-utterance blip, want none", len(got))
	}
}

func TestEnergyVADIgnoresSilence(t *testing.T) {
	t.Parallel()

	v := pipeline.NewEnergyVAD(pipeline.CallVAD())

	for pts := time.Duration(0); pts < 3*time.Second; pts += pipeline.FrameDuration {
		if got := v.Feed(frame(0, pts)); len(got) != 0 {
			t.Fatalf("silence produced an utterance at %v", pts)
		}
	}
}

func BenchmarkVADFeedSilence(b *testing.B) {
	v := pipeline.NewEnergyVAD(pipeline.BroadcastVAD())
	silent := core.PCM{Data: make([]int16, pipeline.FrameSamples), Rate: pipeline.SampleRate, Ch: 1}

	b.ReportAllocs()

	for b.Loop() {
		if got := v.Feed(silent); got != nil {
			b.Fatal("silence emitted an utterance")
		}
	}
}
