package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// countingCloser records how often the encoder feed closes its writer.
type countingCloser struct{ closes int }

func (*countingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *countingCloser) Close() error              { c.closes++; return nil }

// failingCloser reports a close failure.
type failingCloser struct{ countingCloser }

func (f *failingCloser) Close() error {
	f.closes++

	return errors.New("boom")
}

// blockingCloser makes sink finalization observable and controllable.
type blockingCloser struct {
	wrote        chan struct{}
	closeStarted chan struct{}
	allowClose   chan struct{}
	closed       chan struct{}
	wroteOnce    sync.Once
	writer       recordingWriter
}

func (w *blockingCloser) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.wroteOnce.Do(func() { close(w.wrote) })

	return n, err
}

func (w *blockingCloser) Close() error {
	close(w.closeStarted)
	<-w.allowClose
	close(w.closed)

	return nil
}

func idleMixer() *pipeline.Mixer {
	return pipeline.NewMixer(pipeline.NewTrack(), pipeline.NewTrack(), -15)
}

// TestFeedClosesItsWriterExactlyOnce: a second close would re-reap the
// encoder process.
func TestFeedClosesItsWriterExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &countingCloser{}
	if err := feed(ctx, w, idleMixer(), false); err != nil {
		t.Fatalf("feed on an ended context = %v, want nil", err)
	}

	if w.closes != 1 {
		t.Fatalf("writer closed %d times, want exactly once", w.closes)
	}
}

// TestFeedReportsCloseFailure: a failed drain must surface, not vanish.
func TestFeedReportsCloseFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := feed(ctx, &failingCloser{}, idleMixer(), false)
	if err == nil || !strings.Contains(err.Error(), "close encoder feed") {
		t.Fatalf("feed = %v, want the close failure surfaced", err)
	}
}

func TestWaitPlayoutReturnsAfterFinalChunkAndSinkClose(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	bed.Append(0, samples(1000, chunkSamples))
	bed.Finish()
	voice := pipeline.NewTrack()
	voice.Append(0, samples(9000, chunkSamples))
	voice.Finish()

	template := pipeline.NewMixer(bed, voice, math.Inf(-1))
	cursor := template.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}

	ticks := make(chan time.Time)
	sink := &blockingCloser{
		wrote:        make(chan struct{}),
		closeStarted: make(chan struct{}),
		allowClose:   make(chan struct{}),
		closed:       make(chan struct{}),
	}
	feedDone := make(chan error, 1)
	go func() { feedDone <- feedTicks(t.Context(), sink, cursor, false, ticks) }()

	waitDone := make(chan error, 1)
	go func() { waitDone <- template.WaitPlayout(t.Context()) }()

	ticks <- time.Time{}
	<-sink.wrote
	if sink.writer.bytes != chunkSamples*2 || !sink.writer.nonZero {
		t.Fatalf("final write = %d bytes, non-zero %v", sink.writer.bytes, sink.writer.nonZero)
	}

	// The next pull observes EOF. feedTicks must then close the encoder input,
	// and WaitPlayout must still wait while that close is blocked.
	ticks <- time.Time{}
	<-sink.closeStarted
	select {
	case err := <-waitDone:
		t.Fatalf("WaitPlayout returned during sink close: %v", err)
	default:
	}
	select {
	case err := <-feedDone:
		t.Fatalf("feed returned during sink close: %v", err)
	default:
	}

	close(sink.allowClose)
	<-sink.closed
	if err := <-feedDone; err != nil {
		t.Fatalf("feedTicks: %v", err)
	}
	if err := <-waitDone; err != nil {
		t.Fatalf("WaitPlayout: %v", err)
	}
}

func samples(value int16, count int) []int16 {
	out := make([]int16, count)
	for i := range out {
		out[i] = value
	}

	return out
}

// recordingWriter measures what a feed delivers.
type recordingWriter struct {
	bytes   int
	nonZero bool
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.bytes += len(p)
	for _, b := range p {
		if b != 0 {
			r.nonZero = true

			break
		}
	}

	return len(p), nil
}

// TestPaceFillsIdleTicksForDeviceSinks: an audio queue that starts dry
// wedges and never plays later takes; device feeds carry silence from the
// first tick.
func TestPaceFillsIdleTicksForDeviceSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*chunk)
	defer cancel()

	w := &recordingWriter{}
	if err := pace(ctx, w, idleMixer(), true); err != nil {
		t.Fatalf("pace = %v, want nil", err)
	}
	if w.bytes == 0 {
		t.Fatal("device feed wrote nothing while the mixer was idle")
	}
	if w.nonZero {
		t.Fatal("idle fill must be pure silence")
	}
}

func TestPaceDeviceRecoversWithVoiceAfterIdle(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	voice := pipeline.NewTrack()
	mixer := pipeline.NewMixer(bed, voice, math.Inf(-1)).Live()

	ctx, cancel := context.WithTimeout(context.Background(), 14*chunk)
	defer cancel()

	added := make(chan struct{})
	go func() {
		defer close(added)
		timer := time.NewTimer(8 * chunk)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		bed.Append(0, make([]int16, 4*chunkSamples))
		take := make([]int16, 4*chunkSamples)
		for i := range take {
			take[i] = 12000
		}
		voice.Append(0, take)
	}()

	w := &recordingWriter{}
	if err := pace(ctx, w, mixer, true); err != nil {
		t.Fatalf("pace = %v, want nil", err)
	}
	<-added
	if !w.nonZero {
		t.Fatal("device feed did not recover with translated voice after idle")
	}
}

// TestPaceKeepsAnchoredStartForRecordedSinks: HLS and network encoders
// must not receive leading silence that would shift their timeline.
func TestPaceKeepsAnchoredStartForRecordedSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*chunk)
	defer cancel()

	w := &recordingWriter{}
	if err := pace(ctx, w, idleMixer(), false); err != nil {
		t.Fatalf("pace = %v, want nil", err)
	}
	if w.bytes != 0 {
		t.Fatalf("anchored feed wrote %d bytes before the mixer anchor", w.bytes)
	}
}

func TestEncoderJobKeySeparatesTargetsWithoutLeakingThem(t *testing.T) {
	t.Parallel()

	first := encoderJobKey("push", "call", "en", "device://audio/1")
	second := encoderJobKey("push", "call", "en", "device://video/prukka")
	if first == second {
		t.Fatal("distinct push targets share a job key")
	}
	if strings.Contains(first, "device://") || strings.Contains(second, "device://") {
		t.Fatalf("job keys expose target URLs: %q %q", first, second)
	}
	if got := encoderJobKey("push", "call", "en", "device://audio/1"); got != first {
		t.Fatalf("same target key = %q, want stable %q", got, first)
	}
}

func TestPushTargetLimitIsBoundedPerPair(t *testing.T) {
	t.Parallel()

	r := &Registry{jobs: map[string]job{}}
	for i := range maxPushTargetsPerPair {
		id := encoderJobKey("push", "call", "en", fmt.Sprintf("target-%d", i))
		r.jobs[id] = job{}
	}

	if _, err := r.jobIDLocked("push", "call", "en", "overflow"); err == nil {
		t.Fatal("push target limit accepted an additional target")
	}
	if _, err := r.jobIDLocked("push", "call", "it", "independent"); err != nil {
		t.Fatalf("one language consumed another language's target budget: %v", err)
	}
	existing := encoderJobKey("push", "call", "en", "target-0")
	if got, err := r.jobIDLocked("push", "call", "en", "target-0"); err != nil || got != existing {
		t.Fatalf("restarting an existing target = (%q, %v), want %q", got, err, existing)
	}
}

func TestNetworkMuxMatchesTheTransport(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]string{
		"rtmp://example.test/live/key":  "flv",
		"rtmps://example.test/live/key": "flv",
		"srt://example.test:9000":       "mpegts",
	} {
		got, err := networkMux(target)
		if err != nil || got != want {
			t.Fatalf("networkMux(%q) = (%q, %v), want %q", target, got, err, want)
		}
	}

	for _, target := range []string{"/tmp/output.flv", "file:///tmp/output.flv", "https://example.test/live"} {
		if _, err := networkMux(target); err == nil {
			t.Fatalf("networkMux accepted unsupported target %q", target)
		}
	}
}

func TestDropWaitsForEncoderTeardown(t *testing.T) {
	t.Parallel()

	r := &Registry{
		mixers: map[string]*pipeline.Mixer{}, jobs: map[string]job{}, gates: map[string]gate{},
	}
	canceled := make(chan struct{})
	done := make(chan struct{})
	r.jobs["hls:demo/en"] = job{
		cancel: func() { close(canceled) },
		done:   done,
	}

	returned := make(chan struct{})
	go func() {
		r.Drop("demo")
		close(returned)
	}()
	<-canceled
	select {
	case <-returned:
		t.Fatal("Drop returned before the encoder stopped")
	default:
	}
	close(done)
	<-returned
}

func TestWaitPlayoutFreezesAdmissionAndWaitsForRegisteredOutputs(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	template := idleMixer()
	r.Create("demo", "en", template)
	cursor := template.Cursor()
	if !cursor.BeginPlayout() {
		t.Fatal("cursor registration failed")
	}

	jobDone := make(chan struct{})
	r.mu.Lock()
	r.jobs["hls:demo/en"] = job{audio: true, done: jobDone, cancel: func() {}}
	r.mu.Unlock()

	waited := make(chan error, 1)
	go func() { waited <- r.WaitPlayout(t.Context(), "demo") }()

	deadline := time.Now().Add(time.Second)
	for {
		r.mu.RLock()
		finishing := r.gates["demo"].finishing
		r.mu.RUnlock()
		if finishing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("WaitPlayout did not close session admission")
		}
		time.Sleep(time.Millisecond)
	}

	started := false
	err := r.launch("hls", "demo", "en", "", func(context.Context) (io.WriteCloser, error) {
		started = true

		return &countingCloser{}, nil
	})
	if !errors.Is(err, core.ErrNotReady) || started {
		t.Fatalf("launch while finishing = (started %v, err %v), want rejected", started, err)
	}

	cursor.ReleasePlayout()
	select {
	case err := <-waited:
		t.Fatalf("WaitPlayout ignored running encoder: %v", err)
	default:
	}
	close(jobDone)
	if err := <-waited; err != nil {
		t.Fatalf("WaitPlayout: %v", err)
	}
}

func TestWaitPlayoutContextBoundsEncoderTeardown(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	r.Create("demo", "en", idleMixer())
	r.mu.Lock()
	r.jobs["hls:demo/en"] = job{audio: true, done: make(chan struct{}), cancel: func() {}}
	r.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.WaitPlayout(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitPlayout = %v, want context.Canceled", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
