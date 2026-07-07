package pipeline

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type manualClock struct {
	nowAt time.Time
}

func (c *manualClock) now() time.Time {
	return c.nowAt
}

func (c *manualClock) advance(d time.Duration) {
	c.nowAt = c.nowAt.Add(d)
}

func markedSamples(value int16, d time.Duration) []int16 {
	samples := make([]int16, samplesFor(d))
	for i := range samples {
		samples[i] = value
	}

	return samples
}

func TestMixerWaitsForPlayoutAndKeepsVoiceWithinDelay(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(8 * time.Second)
	bed.Append(8*time.Second, markedSamples(0, 9*time.Second))

	voice := NewTrack()
	mixer := NewMixer(bed, voice, -15)

	if _, ok := mixer.Pull(samplesFor(100 * time.Millisecond)); ok {
		t.Fatal("mixer started before delay D")
	}

	clock.advance(8*time.Second - time.Millisecond)
	voice.Append(8*time.Second, markedSamples(9000, 100*time.Millisecond))

	if _, ok := mixer.Pull(samplesFor(100 * time.Millisecond)); ok {
		t.Fatal("mixer started before the final millisecond of D")
	}

	clock.advance(time.Millisecond)
	out, ok := mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok {
		t.Fatal("mixer did not start when PTS D became due")
	}
	if out.PTS != 8*time.Second {
		t.Fatalf("PTS = %v, want 8s", out.PTS)
	}
	if out.Data[0] != 9000 {
		t.Fatalf("first sample = %d, want the voice appended during D", out.Data[0])
	}
}

func TestMixerDoesNotAdvancePastTheLiveEdge(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(0)
	bed.Append(0, markedSamples(1, 50*time.Millisecond))

	mixer := NewMixer(bed, NewTrack(), -15)
	chunk := samplesFor(100 * time.Millisecond)
	if _, ok := mixer.Pull(chunk); ok {
		t.Fatal("mixer fabricated a window beyond the live edge")
	}

	// A complete window alone is not enough: without a live cushion the
	// consumer races the writer chunk by chunk and playout stutters.
	bed.Append(50*time.Millisecond, markedSamples(2, 50*time.Millisecond))
	if _, ok := mixer.Pull(chunk); ok {
		t.Fatal("mixer released a window flush with the live edge")
	}

	bed.Append(100*time.Millisecond, markedSamples(3, minPlayoutCushion))
	out, ok := mixer.Pull(chunk)
	if !ok {
		t.Fatal("mixer did not release a complete acquired window")
	}
	if out.PTS != 0 || out.Data[0] != 1 || out.Data[chunk-1] != 2 {
		t.Fatalf("window = PTS %v, edges %d/%d", out.PTS, out.Data[0], out.Data[chunk-1])
	}
}

// TestMixerMutedBedPassesOnlyTheVoice: bed=off (calls) must exclude the
// original audio entirely — the sidechain would otherwise release it back
// to full volume whenever the dubbed voice pauses.
func TestMixerMutedBedPassesOnlyTheVoice(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(0)
	bed.Append(0, markedSamples(9000, 200*time.Millisecond+minPlayoutCushion))

	voice := NewTrack()
	voice.Append(100*time.Millisecond, markedSamples(7000, 100*time.Millisecond))
	mixer := NewMixer(bed, voice, math.Inf(-1))
	out, ok := mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok {
		t.Fatal("muted mixer did not release the window")
	}
	for i, sample := range out.Data {
		if sample != 0 {
			t.Fatalf("sample %d = %d, want pure silence with a muted bed and no voice", i, sample)
		}
	}

	clock.advance(100 * time.Millisecond)
	out, ok = mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok {
		t.Fatal("muted mixer did not release the translated window")
	}
	if out.Data[0] != 7000 {
		t.Fatalf("translated voice was not passed cleanly: ready=%v first=%d", ok, out.Data[0])
	}
}

func TestLiveMixerKeepsConfiguredDelay(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(8 * time.Second)
	bed.Append(8*time.Second, markedSamples(1, 9*time.Second))
	clock.advance(9 * time.Second)

	out, ok := NewMixer(bed, NewTrack(), -15).Live().Pull(samplesFor(100 * time.Millisecond))
	if !ok || out.PTS != 8700*time.Millisecond {
		t.Fatalf("live delayed pull = (%v, %v), want (8.7s, true)", out.PTS, ok)
	}
}

func TestLiveMixerReanchorsAfterAJump(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(0)
	bed.Append(0, markedSamples(1, time.Second))
	clock.advance(time.Second)

	mixer := NewMixer(bed, NewTrack(), -15).Live()
	first, ok := mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok || first.PTS != 400*time.Millisecond {
		t.Fatalf("first live pull = (%v, %v), want (400ms, true)", first.PTS, ok)
	}

	bed.Append(10*time.Second, markedSamples(2, time.Second))
	clock.advance(11 * time.Second)
	second, ok := mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok || second.PTS != 10400*time.Millisecond {
		t.Fatalf("reanchored pull = (%v, %v), want (10.4s, true)", second.PTS, ok)
	}
}

func TestMixerSpillsLateVoicePastRenderedAudio(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(time.Second)
	bed.Append(time.Second, markedSamples(0, 2*time.Second))

	voice := NewTrack()
	mixer := NewMixer(bed, voice, -15)
	chunk := samplesFor(100 * time.Millisecond)

	clock.advance(time.Second)
	if _, ok := mixer.Pull(chunk); !ok {
		t.Fatal("first playout window was not ready")
	}

	placed := voice.Append(time.Second, markedSamples(7000, 100*time.Millisecond))
	if placed != 1100*time.Millisecond {
		t.Fatalf("late voice placed at %v, want 1.1s", placed)
	}

	clock.advance(100 * time.Millisecond)
	out, ok := mixer.Pull(chunk)
	if !ok || out.Data[0] != 7000 {
		t.Fatalf("late voice was lost: ready=%v first=%d", ok, out.Data[0])
	}
}

func TestMixerDrainsVoicePastTheFinishedBed(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(time.Second)
	bed.Append(time.Second, markedSamples(0, 100*time.Millisecond))
	bed.finish()

	voice := NewTrack()
	voice.Append(1100*time.Millisecond, markedSamples(7000, 100*time.Millisecond))
	mixer := NewMixer(bed, voice, -15)
	clock.advance(time.Second)

	if _, ok := mixer.Pull(samplesFor(100 * time.Millisecond)); !ok {
		t.Fatal("finished bed window was not ready")
	}
	clock.advance(100 * time.Millisecond)
	out, ok := mixer.Pull(samplesFor(100 * time.Millisecond))
	if !ok || out.Data[0] != 7000 {
		t.Fatalf("voice tail was truncated: ready=%v first=%d", ok, out.Data[0])
	}
}

func TestMixerWaitPlayoutNeedsConsumptionAndSinkAcknowledgement(t *testing.T) {
	t.Parallel()

	bed := NewTrack()
	bed.Append(0, markedSamples(1000, 100*time.Millisecond))
	bed.Finish()

	voice := NewTrack()
	voice.Append(0, markedSamples(7000, 100*time.Millisecond))
	voice.Finish()

	template := NewMixer(bed, voice, -15)
	cursor := template.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor was rejected before finite playout was sealed")
	}
	_ = template.Cursor() // An unused cursor never joins the active set.

	returned := make(chan error, 1)
	go func() { returned <- template.WaitPlayout(t.Context()) }()

	waitUntilSealed(t, template.group)

	select {
	case err := <-returned:
		t.Fatalf("WaitPlayout returned before consumption: %v", err)
	default:
	}

	buf := make([]int16, samplesFor(100*time.Millisecond))
	pcm, status := cursor.NextInto(buf)
	if status != PullReady || pcm.Data[0] == 0 {
		t.Fatalf("final chunk = status %v, first sample %d", status, pcm.Data[0])
	}
	if _, status = cursor.NextInto(buf); status != PullEOF {
		t.Fatalf("status after final chunk = %v, want EOF", status)
	}

	select {
	case err := <-returned:
		t.Fatalf("WaitPlayout returned before sink acknowledgement: %v", err)
	default:
	}

	cursor.ReleasePlayout()
	if err := <-returned; err != nil {
		t.Fatalf("WaitPlayout after acknowledgement: %v", err)
	}
}

func waitUntilSealed(t *testing.T, group *playoutGroup) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		group.mu.Lock()
		sealed := group.sealed
		group.mu.Unlock()
		if sealed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("WaitPlayout did not seal the consumer set")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMixerWaitPlayoutHonorsCancellation(t *testing.T) {
	t.Parallel()

	bed := NewTrack()
	voice := NewTrack()
	mixer := NewMixer(bed, voice, -15)
	cursor := mixer.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}
	t.Cleanup(cursor.ReleasePlayout)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := mixer.WaitPlayout(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitPlayout = %v, want context.Canceled", err)
	}
}

func TestLiveMixerReportsPendingUntilBothTracksFinish(t *testing.T) {
	t.Parallel()

	bed := NewTrack()
	voice := NewTrack()
	mixer := NewMixer(bed, voice, -15).Live()
	buf := make([]int16, samplesFor(100*time.Millisecond))

	if _, status := mixer.NextInto(buf); status != PullPending {
		t.Fatalf("empty live mixer = %v, want pending", status)
	}
	bed.Finish()
	if _, status := mixer.NextInto(buf); status != PullPending {
		t.Fatalf("live mixer with unfinished voice = %v, want pending", status)
	}
	voice.Finish()
	if _, status := mixer.NextInto(buf); status != PullEOF {
		t.Fatalf("fully finished empty mixer = %v, want EOF", status)
	}
	mixer.ReleasePlayout()
}
