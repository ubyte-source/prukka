package pipeline_test

import (
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestMixerWaitsForTheBedAnchor(t *testing.T) {
	t.Parallel()

	m := pipeline.NewMixer(pipeline.NewTrack(), pipeline.NewTrack(), -15)

	if _, ok := pull(m, 160); ok {
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

	out, ok := pull(m, 3*pipeline.SampleRate)
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

	out, ok := pull(m, pipeline.SampleRate/5)
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
	second, _ := pull(m, pipeline.SampleRate/10)
	if second.PTS != out.PTS+200*time.Millisecond {
		t.Fatalf("second PTS = %v, want %v", second.PTS, out.PTS+200*time.Millisecond)
	}
}

func TestMixerPullIntoUsesCallerBuffer(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	bed.Append(0, tone(1234, 160))
	mixer := pipeline.NewMixer(bed, pipeline.NewTrack(), -15)
	dst := make([]int16, 160)

	out, ok := pullInto(mixer, dst)
	if !ok {
		t.Fatal("PullInto returned nothing")
	}
	if &out.Data[0] != &dst[0] {
		t.Fatal("PullInto did not return the caller's buffer")
	}
	if out.Data[0] != 1234 {
		t.Fatalf("first sample = %d, want 1234", out.Data[0])
	}
}

func TestMixerCursorsRenderTheFullTimelineIndependently(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	bed.Append(3*time.Second, tone(1200, pipeline.SampleRate))
	template := pipeline.NewMixer(bed, pipeline.NewTrack(), -15)
	a := template.Cursor()
	b := template.Cursor()

	aFirst, aOK := pull(a, 1600)
	bFirst, bOK := pull(b, 1600)
	if !aOK || !bOK {
		t.Fatal("cursor did not observe the shared bed anchor")
	}

	if aFirst.PTS != 3*time.Second || bFirst.PTS != aFirst.PTS || aFirst.Data[0] != bFirst.Data[0] {
		t.Fatalf("first windows differ: a=%v/%d b=%v/%d",
			aFirst.PTS, aFirst.Data[0], bFirst.PTS, bFirst.Data[0])
	}

	aSecond, _ := pull(a, 1600)
	bSecond, _ := pull(b, 1600)
	if aSecond.PTS != bSecond.PTS || aSecond.PTS != aFirst.PTS+100*time.Millisecond {
		t.Fatalf("independent clocks diverged: a=%v b=%v", aSecond.PTS, bSecond.PTS)
	}
}

// TestMixerDrainsTailOnlyAfterFinish: a live track holds its final window
// inside the playout cushion, waiting for more source audio; once the source
// ends, Finish releases the buffered tail so a finite lane's delayed dub drains
// in full instead of being cut.
func TestMixerDrainsTailOnlyAfterFinish(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	// 200 ms of audio — shorter than the 300 ms cushion, so the whole clip
	// sits inside it.
	bed.Append(0, tone(1000, pipeline.SampleRate/5))
	bed.ConfigurePlayout(0)

	m := pipeline.NewMixer(bed, pipeline.NewTrack(), -15)

	if _, ok := pull(m, 160); ok {
		t.Fatal("Pull released audio inside the cushion before the source finished")
	}

	bed.Finish()

	if _, ok := pull(m, 160); !ok {
		t.Fatal("Pull withheld the buffered tail after Finish")
	}
}

// TestBedAnchorsAtTheDelayedClock: the engine lays the original audio on the
// bed at source PTS + delay, so a source-0 second is heard at 8 s downstream.
func TestBedAnchorsAtTheDelayedClock(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()

	speech := make([]int16, pipeline.SampleRate)
	for i := range speech {
		speech[i] = 1000
	}
	bed.Append(8*time.Second, speech)

	start, ok := bed.Start()
	if !ok || start != 8*time.Second {
		t.Fatalf("bed anchor = %v (ok=%v), want source PTS 0 + delay 8s", start, ok)
	}

	probe := make([]int16, pipeline.SampleRate)
	bed.Window(8*time.Second, probe)
	if probe[0] == 0 {
		t.Fatal("bed missing the speech audio on the delayed clock")
	}
}

func pull(p pipeline.Playout, n int) (core.PCM, bool) {
	pcm, status := p.NextInto(make([]int16, n))

	return pcm, status == pipeline.PullReady
}

func pullInto(p pipeline.Playout, dst []int16) (core.PCM, bool) {
	pcm, status := p.NextInto(dst)

	return pcm, status == pipeline.PullReady
}
