package pipeline

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
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
	return slices.Repeat([]int16{value}, samplesFor(d))
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

	if _, ok := pull(mixer, samplesFor(100*time.Millisecond)); ok {
		t.Fatal("mixer started before delay D")
	}

	clock.advance(8*time.Second - time.Millisecond)
	voice.Append(8*time.Second, markedSamples(9000, 100*time.Millisecond))

	if _, ok := pull(mixer, samplesFor(100*time.Millisecond)); ok {
		t.Fatal("mixer started before the final millisecond of D")
	}

	clock.advance(time.Millisecond)
	out, ok := pull(mixer, samplesFor(100*time.Millisecond))
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

// TestLateCursorSeedsAtTheLiveEdgeNotTheRetentionFloor: every consumer that
// attaches after a session started — a browser opening the stream, a
// re-pushed device, a rebuilt lane — gets a FRESH cursor. Seeding it at the
// oldest retained instant would play tens of seconds stale and, once trim
// slides the base past it, hand the sink silent windows marked ready.
func TestLateCursorSeedsAtTheLiveEdgeNotTheRetentionFloor(t *testing.T) {
	t.Parallel()

	const (
		delay   = 8 * time.Second
		quantum = 100 * time.Millisecond
	)
	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(delay)
	voice := NewTrack()

	// A live session: two minutes of continuous bed, the clock tracking it.
	bed.Append(delay, markedSamples(1000, quantum))
	for elapsed := time.Duration(0); elapsed < 2*time.Minute; elapsed += quantum {
		clock.advance(quantum)
		bed.Append(delay+elapsed+quantum, markedSamples(1000, quantum))
	}

	late := NewMixer(bed, voice, -15).Cursor()
	if !late.BeginPlayout() {
		t.Fatal("late cursor registration failed")
	}
	dst := make([]int16, samplesFor(quantum))
	first, status := late.NextInto(dst)
	if status != PullReady {
		t.Fatalf("late cursor pull = %v, want ready", status)
	}

	// The newest due instant is (elapsed - delay); a cursor at the retention
	// floor would sit a full retention window behind it.
	liveEdge := bed.first + clock.nowAt.Sub(bed.anchoredAt) - delay
	if lag := liveEdge - first.PTS; lag > 2*quantum {
		t.Fatalf("late cursor lag = %v behind the live edge, want at most %v", lag, 2*quantum)
	}
	if first.Data[0] == 0 {
		t.Fatal("late cursor rendered silence from a span the bed never left empty")
	}
}

// TestTrimPastAStalledCursorSnapsItBackIntoTheRetainedWindow: the seed's
// companion. A cursor whose sink stopped pulling — a wedged encoder, a device
// reopening, a paused browser tab — falls behind while the writer runs on,
// until retention drops the very audio its clock still points at. From there
// every render is a silent window the ready gate still calls ready, and since
// the clock advances one quantum per pull while the base advances in real
// time, nothing ever closes the gap: the session is muted for good. Trim
// moving the base past the clock is therefore the one event allowed to move
// it forward — playing late is a defect, playing silence is an outage.
func TestTrimPastAStalledCursorSnapsItBackIntoTheRetainedWindow(t *testing.T) {
	t.Parallel()

	const (
		delay   = 8 * time.Second
		quantum = 100 * time.Millisecond
	)
	clock := &manualClock{nowAt: time.Unix(1, 0)}
	bed := NewTrack()
	bed.clock = clock
	bed.ConfigurePlayout(delay)

	// The bed carries no trim fence — every language's mixer shares it, so no
	// single cursor may pin it — which is what lets retention overtake a
	// stalled one. The session delay doubles as the live cushion, so a
	// zero-delay bed would never release a window to stall in the first place.
	write := func(from, until time.Duration) {
		for elapsed := from; elapsed < until; elapsed += quantum {
			clock.advance(quantum)
			bed.Append(delay+elapsed+quantum, markedSamples(1000, quantum))
		}
	}
	bed.Append(delay, markedSamples(1000, quantum))
	write(0, 30*time.Second)

	cursor := NewMixer(bed, NewTrack(), -15).Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}
	dst := make([]int16, samplesFor(quantum))
	first, status := cursor.NextInto(dst)
	if status != PullReady {
		t.Fatalf("first pull = %v, want ready", status)
	}

	// The sink stops consuming while the session runs on for two more minutes:
	// retention (30s plus the delay, with 15s of slack) then drops everything
	// the stalled clock points at.
	write(30*time.Second, 150*time.Second)
	stalled := first.PTS + quantum
	base, anchored := bed.Start()
	if !anchored || base <= stalled {
		t.Fatalf("retained base = %v (anchored %v), want it past the stalled clock %v", base, anchored, stalled)
	}

	resumed, status := cursor.NextInto(dst)
	if status != PullReady {
		t.Fatalf("resumed pull = %v, want ready", status)
	}
	if resumed.PTS != base {
		t.Fatalf("resumed PTS = %v, want the retained base %v", resumed.PTS, base)
	}
	if resumed.Data[0] == 0 || resumed.Data[len(resumed.Data)-1] == 0 {
		t.Fatalf("resumed window = %d..%d, want the audio the base still retains",
			resumed.Data[0], resumed.Data[len(resumed.Data)-1])
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
	if _, ok := pull(mixer, chunk); ok {
		t.Fatal("mixer fabricated a window beyond the live edge")
	}

	// A complete window alone is not enough: without a live cushion the
	// consumer races the writer chunk by chunk and playout stutters.
	bed.Append(50*time.Millisecond, markedSamples(2, 50*time.Millisecond))
	if _, ok := pull(mixer, chunk); ok {
		t.Fatal("mixer released a window flush with the live edge")
	}

	bed.Append(100*time.Millisecond, markedSamples(3, 300*time.Millisecond))
	out, ok := pull(mixer, chunk)
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
	bed.Append(0, markedSamples(9000, 200*time.Millisecond+300*time.Millisecond))

	voice := NewTrack()
	voice.Append(100*time.Millisecond, markedSamples(7000, 100*time.Millisecond))
	mixer := NewMixer(bed, voice, math.Inf(-1))
	out, ok := pull(mixer, samplesFor(100*time.Millisecond))
	if !ok {
		t.Fatal("muted mixer did not release the window")
	}
	for i, sample := range out.Data {
		if sample != 0 {
			t.Fatalf("sample %d = %d, want pure silence with a muted bed and no voice", i, sample)
		}
	}

	clock.advance(100 * time.Millisecond)
	out, ok = pull(mixer, samplesFor(100*time.Millisecond))
	if !ok {
		t.Fatal("muted mixer did not release the translated window")
	}
	if out.Data[0] != 7000 {
		t.Fatalf("translated voice was not passed cleanly: ready=%v first=%d", ok, out.Data[0])
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
	if _, ok := pull(mixer, chunk); !ok {
		t.Fatal("first playout window was not ready")
	}

	placed := voice.Append(time.Second, markedSamples(7000, 100*time.Millisecond))
	if placed != 1100*time.Millisecond {
		t.Fatalf("late voice placed at %v, want 1.1s", placed)
	}

	clock.advance(100 * time.Millisecond)
	out, ok := pull(mixer, chunk)
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

	if _, ok := pull(mixer, samplesFor(100*time.Millisecond)); !ok {
		t.Fatal("finished bed window was not ready")
	}
	clock.advance(100 * time.Millisecond)
	out, ok := pull(mixer, samplesFor(100*time.Millisecond))
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

func TestTrimNeverCrossesTheLivePlayoutFence(t *testing.T) {
	t.Parallel()

	clock := &manualClock{nowAt: time.Unix(1, 0)}
	voice := NewTrack()
	voice.clock = clock
	voice.ConfigurePlayout(0)

	// The LIVE consumer has played up to 10s; takes keep queueing until the
	// buffer far exceeds retention (30s keep + 15s slack). The fence holds
	// only while the consumer is registered — a departed one must not pin
	// memory, which is the companion contract in track_test.go.
	voice.acquirePlayout()
	voice.reserve(10 * time.Second)
	voice.Append(0, markedSamples(7, 52*time.Second))

	if voice.base > 10*time.Second {
		t.Fatalf("trim crossed the playout fence: base=%v floor=10s", voice.base)
	}
	window := make([]int16, samplesFor(20*time.Millisecond))
	voice.Window(10*time.Second, window)
	if window[0] != 7 {
		t.Fatalf("window at the fence lost audio: %d", window[0])
	}
}

// pull and pullInto adapt a Playout to the (PCM, ready) shape the retained
// mixer tests assert against.
func pull(p Playout, n int) (core.PCM, bool) {
	pcm, status := p.NextInto(make([]int16, n))

	return pcm, status == PullReady
}

func pullInto(p Playout, dst []int16) (core.PCM, bool) {
	pcm, status := p.NextInto(dst)

	return pcm, status == PullReady
}
