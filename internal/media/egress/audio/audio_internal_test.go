package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// countingCloser records how often the encoder feed closes its writer.
type countingCloser struct{ closes int }

// chunk is the default pacing quantum the fixtures tick and time out with.
const chunk = DefaultFeedQuantum

// chunkTickSamples is the per-tick sample count the production ticker uses.
var chunkTickSamples = pipeline.SamplesInQuantum(chunk)

func (*countingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *countingCloser) Close() error              { c.closes++; return nil }

// failingCloser reports a close failure.
type failingCloser struct{ countingCloser }

func (f *failingCloser) Close() error {
	f.closes++

	return errors.New("boom")
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return max(0, len(p)-1), nil }

// closeUnblocksWriter models a bounded synchronous device queue: Write cannot
// observe context directly and only its owner's Close releases it.
type closeUnblocksWriter struct {
	writeStarted chan struct{}
	closed       chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
	mu           sync.Mutex
	closes       int
}

func newCloseUnblocksWriter() *closeUnblocksWriter {
	return &closeUnblocksWriter{writeStarted: make(chan struct{}), closed: make(chan struct{})}
}

func (w *closeUnblocksWriter) Write([]byte) (int, error) {
	w.writeOnce.Do(func() { close(w.writeStarted) })
	<-w.closed

	return 0, io.ErrClosedPipe
}

func (w *closeUnblocksWriter) Close() error {
	w.mu.Lock()
	w.closes++
	w.mu.Unlock()
	w.closeOnce.Do(func() { close(w.closed) })

	return nil
}

func (w *closeUnblocksWriter) closeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.closes
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

func TestWaitPlayoutReturnsAfterFinalChunkAndSinkClose(t *testing.T) {
	t.Parallel()

	bed := pipeline.NewTrack()
	bed.Append(0, samples(1000, chunkTickSamples))
	bed.Finish()
	voice := pipeline.NewTrack()
	voice.Append(0, samples(9000, chunkTickSamples))
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
	go func() {
		defer cursor.ReleasePlayout()
		feedDone <- feedTicks(t.Context(), sink, cursor, false, defaultFeedConfig(), ticks)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- template.WaitPlayout(t.Context()) }()

	ticks <- time.Time{}
	<-sink.wrote
	if sink.writer.bytes != chunkTickSamples*2 || !sink.writer.nonZero {
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
	return slices.Repeat([]int16{value}, count)
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

type cancelingWriter struct {
	cancel context.CancelFunc
	writer recordingWriter
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.cancel()

	return n, err
}

func TestCreateStoresFeedQuantumPerRegistration(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	r.Create("call", "en", idleMixer(), WithFeedQuantum(20*time.Millisecond))
	r.Create("call", "it", idleMixer())

	r.mu.RLock()
	english := r.pairs[key("call", "en")].pacing
	italian := r.pairs[key("call", "it")].pacing
	r.mu.RUnlock()

	if english.quantum != 20*time.Millisecond || english.samples != core.SampleRate/50 {
		t.Fatalf("English feed = %v/%d samples, want 20ms/%d", english.quantum, english.samples, core.SampleRate/50)
	}
	if italian.quantum != DefaultFeedQuantum || italian.samples != chunkTickSamples {
		t.Fatalf("Italian default feed = %v/%d samples, want %v/%d",
			italian.quantum, italian.samples, DefaultFeedQuantum, chunkTickSamples)
	}

	r.Drop("call")
	r.mu.RLock()
	remaining := len(r.pairs)
	r.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("registrations after Drop = %d, want 0", remaining)
	}
}

func TestFeedQuantumRejectsInvalidDurations(t *testing.T) {
	t.Parallel()

	for name, quantum := range map[string]time.Duration{
		"negative":    -time.Millisecond,
		"not aligned": pipeline.SamplePeriod + time.Nanosecond,
		"zero":        0,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatalf("WithFeedQuantum(%v) did not panic", quantum)
				}
			}()

			_ = WithFeedQuantum(quantum)
		})
	}
}

func TestPaceTicksUsesConfiguredEncoderQuantum(t *testing.T) {
	t.Parallel()

	pacing := makeFeedConfig(20 * time.Millisecond)
	bed := pipeline.NewTrack()
	bed.Append(0, samples(1000, pacing.samples))
	bed.Finish()
	voice := pipeline.NewTrack()
	voice.Finish()
	mixer := pipeline.NewMixer(bed, voice, -15)
	t.Cleanup(mixer.ReleasePlayout)

	ticks := make(chan time.Time, 2)
	ticks <- time.Time{}
	ticks <- time.Time{}
	w := &recordingWriter{}
	observedPeak := 0
	if err := paceTicks(t.Context(), w, mixer, false, pacing, ticks, func(pcm core.PCM) {
		observedPeak = pipeline.PeakS16(pcm.Data)
	}); err != nil {
		t.Fatalf("paceTicks = %v", err)
	}
	if w.bytes != pacing.samples*2 || !w.nonZero {
		t.Fatalf("encoded feed = %d bytes, non-zero %v, want %d non-zero bytes",
			w.bytes, w.nonZero, pacing.samples*2)
	}
	if observedPeak != 1000 {
		t.Fatalf("observed accepted PCM peak = %d, want 1000", observedPeak)
	}
}

func TestAudibleTelemetryThresholdRejectsFadeEdgeNoise(t *testing.T) {
	t.Parallel()

	if audibleTelemetryPeak <= 1 || audibleTelemetryPeak >= pipeline.PeakS16([]int16{12000}) {
		t.Fatalf("audible telemetry peak = %d, want above quantization noise and below speech", audibleTelemetryPeak)
	}
}

func TestPaceTicksRejectsShortWrite(t *testing.T) {
	t.Parallel()

	pacing := makeFeedConfig(20 * time.Millisecond)
	bed := pipeline.NewTrack()
	bed.Append(0, samples(1000, pacing.samples))
	bed.Finish()
	voice := pipeline.NewTrack()
	voice.Finish()
	mixer := pipeline.NewMixer(bed, voice, -15)
	t.Cleanup(mixer.ReleasePlayout)

	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	observed := false
	err := paceTicks(t.Context(), shortWriter{}, mixer, false, pacing, ticks, func(core.PCM) {
		observed = true
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("paceTicks short write = %v, want io.ErrShortWrite", err)
	}
	if observed {
		t.Fatal("short write was reported as accepted PCM")
	}
}

func TestPaceTicksUsesConfiguredDeviceQuantumForIdleFill(t *testing.T) {
	t.Parallel()

	pacing := makeFeedConfig(20 * time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	w := &cancelingWriter{cancel: cancel}
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}

	if err := paceTicks(ctx, w, idleMixer(), true, pacing, ticks); err != nil {
		t.Fatalf("paceTicks = %v", err)
	}
	if w.writer.bytes != pacing.samples*2 {
		t.Fatalf("device fill = %d bytes, want %d", w.writer.bytes, pacing.samples*2)
	}
	if w.writer.nonZero {
		t.Fatal("idle device fill was not silence")
	}
}

// TestPaceFillsIdleTicksForDeviceSinks: an audio queue that starts dry
// wedges and never plays later takes; device feeds carry silence from the
// first tick.
func TestPaceFillsIdleTicksForDeviceSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*chunk)
	defer cancel()

	w := &recordingWriter{}
	if err := pace(ctx, w, idleMixer(), true, defaultFeedConfig()); err != nil {
		t.Fatalf("pace = %v, want nil", err)
	}
	if w.bytes == 0 {
		t.Fatal("device feed wrote nothing while the mixer was idle")
	}
	if w.nonZero {
		t.Fatal("idle fill must be pure silence")
	}
}

func TestPaceKeepsAnchoredStartForRecordedSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*chunk)
	defer cancel()

	w := &recordingWriter{}
	if err := pace(ctx, w, idleMixer(), false, defaultFeedConfig()); err != nil {
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

// TestPushTargetBoundIsOneDecisionOverJobsAndRoutes: the pair's target bound
// is a single admission over the UNION of live encoder jobs and remembered
// push routes. The two maps diverge by design — retireJob forgets a dead job
// while Reset keeps its route across a lane restart — so a bound counted over
// one half admits targets the other half already claims and then drops their
// intents on the floor: the caller is told the push was accepted, the target
// streams now, and it is silently absent after the next lane restart.
func TestPushTargetBoundIsOneDecisionOverJobsAndRoutes(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))

	// All but one of the pair's budget as live encoder jobs.
	for i := range maxPushTargetsPerPair - 1 {
		target := fmt.Sprintf("rtmp://127.0.0.1:1/live/job-%d", i)
		start := func(context.Context) (io.WriteCloser, error) { return devNullSink{}, nil }
		if err := r.launchMixFedJob("push", "cast", "en", target, start); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}

	// The registry has no supervisor, so every Push lands as a not-ready
	// intent: how a client fills a pair before its lane is up.
	for i := range 2 * maxPushTargetsPerPair {
		target := fmt.Sprintf("rtmp://127.0.0.1:1/live/route-%d", i)
		err := r.Push("cast", "en", target, "off")
		accepted := err == nil || errors.Is(err, core.ErrNotReady)

		r.mu.RLock()
		_, remembered := r.routes[encoderJobKey("push", "cast", "en", target)]
		r.mu.RUnlock()

		if accepted != remembered {
			t.Fatalf("Push(%q) = %v with remembered=%v; an accepted push must always be remembered",
				target, err, remembered)
		}
	}

	if got := countPushTargets(r, "cast", "en"); got != maxPushTargetsPerPair {
		t.Fatalf("pair holds %d push targets, want the bound of %d", got, maxPushTargetsPerPair)
	}
}

// countPushTargets counts the distinct push targets one pair holds across both
// maps — the population the bound governs.
func countPushTargets(r *Registry, session, lang string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	prefix := pushTargetPrefix(key(session, lang))
	seen := map[string]struct{}{}
	for id := range r.jobs {
		if strings.HasPrefix(id, prefix) {
			seen[id] = struct{}{}
		}
	}
	for id := range r.routes {
		if strings.HasPrefix(id, prefix) {
			seen[id] = struct{}{}
		}
	}

	return len(seen)
}

// TestCreateAndResetAcrossSessionsNeverCouple: one session's teardown must
// neither crash nor wait on another session's replay scheduling. The process-
// wide replay WaitGroup this replaces panicked here ("Add called concurrently
// with Wait") once two lanes interleaved Create with a foreign Reset.
func TestCreateAndResetAcrossSessionsNeverCouple(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())

	var storm sync.WaitGroup
	for _, session := range []string{"alpha", "beta"} {
		storm.Go(func() {
			for range 200 {
				r.Create(session, "en", idleMixer())
				r.Reset(session)
			}
		})
	}
	storm.Wait()

	r.Create("alpha", "en", idleMixer())
	r.mu.RLock()
	_, alive := r.gates["alpha"]
	r.mu.RUnlock()
	if !alive {
		t.Fatal("registry unusable after concurrent Create/Reset storm")
	}
}

func TestDropWaitsForEncoderTeardown(t *testing.T) {
	t.Parallel()

	r := &Registry{
		pairs: map[string]registration{}, jobs: map[string]job{}, gates: map[string]*gate{},
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

// TestSessionTeardownSparesPrefixSharingSiblings: "cast" and "cast-2" are
// both legal slugs, and pairOwnedBy, jobOwnedBy and sessionPushPrefix keep
// them apart only because each matches on the "session/" boundary. Without it
// a Reset or Drop deletes a bystander session's pair, cancels its encoder
// jobs and forgets its remembered routes — silently, since neither call
// reports anything. Routes are invisible from the public facade, so the guard
// is asserted here over all three predicates at once.
func TestSessionTeardownSparesPrefixSharingSiblings(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	r.Create("cast", "en", idleMixer())
	r.Create("cast-2", "en", idleMixer())

	ownJob, siblingJob := encoderJobKey("hls", "cast", "en", ""), encoderJobKey("hls", "cast-2", "en", "")
	ownRoute := encoderJobKey("push", "cast", "en", "rtmp://127.0.0.1:1/live/own")
	siblingRoute := encoderJobKey("push", "cast-2", "en", "rtmp://127.0.0.1:1/live/sibling")
	siblingCanceled := false
	r.mu.Lock()
	r.jobs[ownJob] = job{cancel: func() {}}
	r.jobs[siblingJob] = job{cancel: func() { siblingCanceled = true }}
	r.routes[ownRoute] = pushRoute{session: "cast", lang: "en"}
	r.routes[siblingRoute] = pushRoute{session: "cast-2", lang: "en"}
	r.mu.Unlock()

	r.Reset("cast") // a lane restart: pairs and jobs go, routes stay
	r.mu.RLock()
	_, routeSurvivedReset := r.routes[siblingRoute]
	r.mu.RUnlock()
	if !routeSurvivedReset {
		t.Fatal(`Reset("cast") forgot the remembered route of "cast-2"`)
	}

	r.Drop("cast") // the session ends: its own routes go too
	r.mu.RLock()
	_, siblingPair := r.pairs[key("cast-2", "en")]
	_, siblingJobKept := r.jobs[siblingJob]
	_, siblingRouteKept := r.routes[siblingRoute]
	_, ownPair := r.pairs[key("cast", "en")]
	_, ownJobKept := r.jobs[ownJob]
	_, ownRouteKept := r.routes[ownRoute]
	r.mu.RUnlock()

	if !siblingPair || !siblingJobKept || !siblingRouteKept || siblingCanceled {
		t.Errorf("sibling after tearing down \"cast\": pair=%v job=%v route=%v canceled=%v, want all kept",
			siblingPair, siblingJobKept, siblingRouteKept, siblingCanceled)
	}
	if ownPair || ownJobKept || ownRouteKept {
		t.Errorf("dropped session kept pair=%v job=%v route=%v, want every one gone",
			ownPair, ownJobKept, ownRouteKept)
	}
}

// parkedVideoSource parks the first VideoPlaylist lookup — a call dispatchPush
// makes for every network target — so a test can hold one Push RPC inside its
// dispatch for as long as it likes.
type parkedVideoSource struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (v *parkedVideoSource) VideoPlaylist(string) (string, bool) {
	v.once.Do(func() { close(v.entered) })
	<-v.release

	return "", false
}

func (*parkedVideoSource) CueFile(string, string) (string, bool) { return "", false }

// TestDropForgetsTheIntentOfAPushItRanThrough: an operator deletes a session
// while a Push RPC for it is in flight. The RPC passed the gateway's session
// check and is then parked inside its dispatch for the whole teardown — Reset
// drains encoder jobs and takes seconds — so it reaches the intent recording
// only after Drop's route sweep already ran. Nothing ever deletes that entry:
// the sweep is the single delete site and Reset preserves routes by design, so
// it grows the map forever and, worse, silently resurrects the target the next
// time the slug is created.
func TestDropForgetsTheIntentOfAPushItRanThrough(t *testing.T) {
	t.Parallel()

	video := &parkedVideoSource{entered: make(chan struct{}), release: make(chan struct{})}
	r := NewRegistry(t.Context(), nil, video, discardLogger())
	r.Create("cast", "en", idleMixer())

	pushed := make(chan error, 1)
	go func() { pushed <- r.Push("cast", "en", "rtmp://127.0.0.1:1/live/key", "off") }()
	<-video.entered

	r.Drop("cast")
	close(video.release)

	if err := <-pushed; !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("push into a dropped session = %v, want ErrNotReady", err)
	}

	r.mu.RLock()
	kept := len(r.routes)
	r.mu.RUnlock()
	if kept != 0 {
		t.Fatalf("routes after Drop = %d, want 0: the push re-recorded its intent after the sweep", kept)
	}
}

// TestPushBeforeTheLanePublishesItsPairRemembersTheIntent is the boundary of
// the rule above. A push that arrives before the lane registers its first pair
// finds no session gate either — that is the ordinary "not ready yet" case the
// gateway admits while a session is starting — and it MUST be remembered, or
// Create would have nothing to replay onto the fresh mixers. Only a push that
// crossed a teardown is refused.
func TestPushBeforeTheLanePublishesItsPairRemembersTheIntent(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())

	if err := r.Push("cast", "en", "rtmp://127.0.0.1:1/live/key", "off"); !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("push before the lane is up = %v, want ErrNotReady", err)
	}

	r.mu.RLock()
	kept := len(r.routes)
	r.mu.RUnlock()
	if kept != 1 {
		t.Fatalf("routes = %d, want the not-ready intent kept for the lane to replay", kept)
	}
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
	r.jobs["hls:demo/en"] = job{consumesMix: true, done: jobDone, cancel: func() {}}
	r.mu.Unlock()

	waited := make(chan error, 1)
	go func() { waited <- r.WaitPlayout(t.Context(), "demo") }()

	testkit.Eventually(t, time.Second, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()

		return r.gates["demo"].finishing
	}, "WaitPlayout did not close session admission")

	started := false
	err := r.launchMixFedJob("hls", "demo", "en", "", func(context.Context) (io.WriteCloser, error) {
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
	r.jobs["hls:demo/en"] = job{consumesMix: true, done: make(chan struct{}), cancel: func() {}}
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

// devNullSink accepts every write and closes cleanly.
type devNullSink struct{}

func (s devNullSink) Write(p []byte) (int, error) { return len(p), nil }

func (s devNullSink) Close() error { return nil }

// observedSink also exposes writes, proving a reopened encoder remains fed
// instead of merely being constructed and immediately retired.
type observedSink struct {
	writes chan []byte
}

func newObservedSink() *observedSink {
	return &observedSink{writes: make(chan []byte, 8)}
}

func (s *observedSink) Write(p []byte) (int, error) {
	payload := append([]byte(nil), p...)
	select {
	case s.writes <- payload:
	default:
	}

	return len(p), nil
}

func (*observedSink) Close() error { return nil }

func awaitObservedSink(t *testing.T, opened <-chan *observedSink, failure string) *observedSink {
	t.Helper()

	select {
	case sink := <-opened:
		return sink
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}

	return nil
}

func assertObservedWrite(t *testing.T, sink *observedSink, want int, failure string) {
	t.Helper()

	select {
	case payload := <-sink.writes:
		if len(payload) != want {
			t.Fatalf("%s: got %d bytes, want %d", failure, len(payload), want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}
}

// TestLaunchReopensDeviceSinkOnConfigurationChange: another application
// switching the device's nominal sample rate kills a long-lived output
// queue silently; the watcher must reopen the sink on the same job and keep
// feeding its cursor instead of leaving the call mute.
func TestLaunchReopensDeviceSinkOnConfigurationChange(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)
	deviceWatchOverride.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { deviceWatchOverride.Store(0) })

	var stamp atomic.Value
	var reads atomic.Int32
	stamp.Store("uid@16000")
	stampFn := func(string) (string, bool) {
		reads.Add(1)
		current, ok := stamp.Load().(string)

		return current, ok
	}

	opened := make(chan *observedSink, 4)
	start := func(context.Context) (io.WriteCloser, error) {
		sink := newObservedSink()
		opened <- sink

		return sink, nil
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger(), WithConfigStamp(stampFn))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	first := awaitObservedSink(t, opened, "initial device sink did not open")
	assertObservedWrite(t, first, 2*pacing.samples, "initial device sink received no PCM quantum")

	// Flip only after the feed sampled its baseline and the watcher ticked
	// at least once, so the change is observed as a change.
	testkit.Eventually(t, 5*time.Second, func() bool {
		return reads.Load() >= 2
	}, "watcher never sampled the fingerprint")
	stamp.Store("uid@48000")
	second := awaitObservedSink(t, opened, "device reconfiguration did not reopen the sink")
	assertObservedWrite(t, second, 2*pacing.samples, "reopened device sink received no PCM quantum")
}

// TestFeedWatchedStaysInertWithoutAFingerprint: non-device jobs and
// platforms without a stamp must feed exactly as before.
func TestFeedWatchedStaysInertWithoutAFingerprint(t *testing.T) {
	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithConfigStamp(func(string) (string, bool) { return "", false }))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := r.feedWatched(ctx, devNullSink{}, idleMixer(), true, feedConfig{
		quantum: time.Millisecond, samples: 16,
	}, "device://audio/3?label=X")
	if err != nil {
		t.Fatalf("inert feedWatched = %v", err)
	}
}

// TestFeedWatchedSignalsReconfiguration isolates the watcher: a stamp change
// must surface as errDeviceReconfigured while the job context stays alive.
func TestFeedWatchedSignalsReconfiguration(t *testing.T) {
	deviceWatchOverride.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { deviceWatchOverride.Store(0) })

	var stamp atomic.Value
	stamp.Store("uid@16000")
	stampFn := func(string) (string, bool) {
		current, ok := stamp.Load().(string)

		return current, ok
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		stamp.Store("uid@48000")
	}()

	r := NewRegistry(t.Context(), nil, nil, discardLogger(), WithConfigStamp(stampFn))
	err := r.feedWatched(t.Context(), devNullSink{}, idleMixer().Cursor(), true, feedConfig{
		quantum: 5 * time.Millisecond, samples: 80,
	}, "device://audio/3?label=X")
	if !errors.Is(err, errDeviceReconfigured) {
		t.Fatalf("feedWatched = %v, want errDeviceReconfigured", err)
	}
}

func TestFeedWatchedAcquiresPendingLabeledBaseline(t *testing.T) {
	deviceWatchOverride.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { deviceWatchOverride.Store(0) })

	var reads atomic.Int32
	stampFn := func(string) (string, bool) {
		switch reads.Add(1) {
		case 1, 2:
			return "", false
		case 3:
			return "uid@48000#3", true
		default:
			return "uid@48000#4", true
		}
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger(), WithConfigStamp(stampFn))
	err := r.feedWatched(t.Context(), devNullSink{}, idleMixer().Cursor(), true, feedConfig{
		quantum: 5 * time.Millisecond, samples: 80,
	}, "device://audio/3?label=Prukka+Microphone")
	if !errors.Is(err, errDeviceReconfigured) {
		t.Fatalf("pending-baseline feedWatched = %v, want errDeviceReconfigured", err)
	}
	if got := reads.Load(); got < 4 {
		t.Fatalf("fingerprint reads = %d, want pending, baseline and changed samples", got)
	}
}

// countingStart opens observable no-op sinks: re-attach tests count them.
func countingStart(opened chan struct{}) func(context.Context) (io.WriteCloser, error) {
	return func(context.Context) (io.WriteCloser, error) {
		opened <- struct{}{}

		return devNullSink{}, nil
	}
}

// TestEncoderJobReattachesAfterPairRebuild: a restarted lane replaces the
// pair's mixers and finishes the old tracks; the running push must follow
// onto the rebuilt template instead of retiring while the session runs.
func TestEncoderJobReattachesAfterPairRebuild(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bedA, voiceA := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", pipeline.NewMixer(bedA, voiceA, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-opened

	// The lane dies: its engine finishes the old tracks, the cursor drains
	// to EOF, and only later the restarted lane publishes fresh mixers.
	bedA.Finish()
	voiceA.Finish()
	time.Sleep(50 * time.Millisecond)
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))

	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the rebuilt pair did not re-attach the encoder")
	}
}

// TestEncoderJobEndsQuietlyWhenSessionFinishes: EOF during the finishing
// drain is the ordinary end of a finite session, never a re-attach wait.
func TestEncoderJobEndsQuietlyWhenSessionFinishes(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "cast", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-opened

	if _, _, _, ok := r.finishSnapshot("cast"); !ok {
		t.Fatal("finishSnapshot refused the session")
	}
	bed.Finish()
	voice.Finish()

	testkit.Eventually(t, 5*time.Second, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()

		return len(r.jobs) == 0
	}, "finishing session did not conclude the encoder job")
	select {
	case <-opened:
		t.Fatal("finishing session must not re-open the sink")
	default:
	}
}

// drainedSink reports its own close, which is the moment the paced feed
// observed EOF and the job left it for the re-attach wait.
type drainedSink struct {
	mu     sync.Mutex
	closes int
}

func (*drainedSink) Write(p []byte) (int, error) { return len(p), nil }

func (s *drainedSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++

	return nil
}

func (s *drainedSink) drained() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closes > 0
}

// TestSessionFinishesUnderAJobAlreadyPollingForItsRebuiltPair: the two ends of
// a finite push meet here. The job's pair drained to EOF, so it is parked in
// the re-attach poll re-reading the session's admission every
// pairRebuildPoll, while WaitPlayout flips that same gate to finishing under
// the registry write lock. Admission therefore has to be evaluated INSIDE the
// snapshot's lock window: a job that polled first and then read the flag
// unlocked races the finish it is waiting for, and the race detector can only
// see it in that order — every other test in this package finishes the session
// before the job ever polls, which orders the write ahead of the read and
// hides it. The end state is the ordinary one: the parked job concludes.
func TestSessionFinishesUnderAJobAlreadyPollingForItsRebuiltPair(t *testing.T) {
	t.Parallel()

	sink := &drainedSink{}
	start := func(context.Context) (io.WriteCloser, error) { return sink, nil }

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "cast", "en", "rtmp://relay.example/live", start); err != nil {
		t.Fatalf("launch: %v", err)
	}

	// EOF ends the feed and closes the sink; only then does the job start
	// polling, so the close is what proves the poll is under way.
	bed.Finish()
	voice.Finish()
	testkit.Eventually(t, 5*time.Second, sink.drained, "the drained pair never released the encoder sink")

	if _, _, _, ok := r.finishSnapshot("cast"); !ok {
		t.Fatal("finishSnapshot refused the session")
	}
	testkit.Eventually(t, 5*time.Second, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()

		return len(r.jobs) == 0
	}, "the polling job outlived the session that finished under it")
}

// TestPushReplacementDoesNotDeadlockAReattachingJob: launch replaces a job
// while holding the write lock and waiting for it to end; a job parked in
// the re-attach wait must yield without ever blocking on that lock.
func TestPushReplacementDoesNotDeadlockAReattachingJob(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	<-opened

	// Drain the first job to EOF so it parks in the re-attach wait.
	bed.Finish()
	voice.Finish()
	time.Sleep(100 * time.Millisecond)

	replaced := make(chan error, 1)
	go func() {
		replaced <- r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start)
	}()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatalf("replacement launch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replacement launch deadlocked against the re-attach wait")
	}
	<-opened
}

// writeExecutable plants one runnable script; the mode travels as a
// parameter so the fixture stays out of static permission findings.
func writeExecutable(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("fixture executable: %v", err)
	}
}

// TestPushRoutesSurviveLaneResetAndRelaunch: a failed lane resets the
// playout tree; the user's push route must relaunch when the restarted lane
// re-creates the pair — and must be forgotten on Drop, the session's end.
func TestPushRoutesSurviveLaneResetAndRelaunch(t *testing.T) {
	if runtime.GOOS == hostos.Windows {
		// The sink start execs the fake ffmpeg, a POSIX shell script Windows
		// cannot run; the route-survival logic under test is platform-independent
		// and is exercised on macOS and Linux.
		t.Skip("fake ffmpeg helper is not a runnable Windows executable")
	}
	t.Parallel()

	fake := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, fake, "#!/bin/sh\nwhile read line; do :; done\n", 0o700)

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.SetSupervisor(ffmpeg.NewSupervisor(fake, discardLogger()))
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))

	// A network sink exercises the route bookkeeping through the (fake)
	// supervisor on every OS; a device:// target would eagerly open a real
	// WASAPI endpoint on Windows, which the runner does not have.
	target := "rtmp://127.0.0.1:1/live/push-reset"
	if err := r.Push("call", "en", target, "off"); err != nil {
		t.Fatalf("push: %v", err)
	}
	jobID := encoderJobKey("push", "call", "en", target)

	// The lane dies and clears its tree; the route intent survives.
	r.Reset("call")
	r.mu.RLock()
	_, jobAlive := r.jobs[jobID]
	_, routeKept := r.routes[jobID]
	r.mu.RUnlock()
	if jobAlive || !routeKept {
		t.Fatalf("after Reset: job=%v route=%v, want job gone and route kept", jobAlive, routeKept)
	}

	// The restarted lane re-registers the pair: the route relaunches.
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	testkit.Eventually(t, 5*time.Second, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		_, relaunched := r.jobs[jobID]

		return relaunched
	}, "route did not relaunch on the rebuilt pair")

	// Drop ends the session for good: the intent must not survive.
	r.Drop("call")
	r.mu.RLock()
	_, routeAfterDrop := r.routes[jobID]
	r.mu.RUnlock()
	if routeAfterDrop {
		t.Fatal("Drop kept the push route")
	}
}

// pace drives paceTicks on a real-time ticker; the production feed builds the
// ticker inline, tests drive it through this helper.
func pace(ctx context.Context, in io.Writer, mixer pipeline.Playout, fill bool, pacing feedConfig) error {
	ticker := time.NewTicker(pacing.quantum)
	defer ticker.Stop()

	return paceTicks(ctx, in, mixer, fill, pacing, ticker.C)
}

// shrinkReopenBackoff makes reopen retries immediate for tests.
func shrinkReopenBackoff(t *testing.T) {
	t.Helper()
	saved := deviceReopenBackoff
	deviceReopenBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { deviceReopenBackoff = saved })
}

// TestDeviceSinkSelfHealsAcrossFailedReopens: a device reconfiguration whose
// first reopen attempts fail must keep retrying and recover once the device
// returns, instead of silently abandoning the route.
func TestDeviceSinkSelfHealsAcrossFailedReopens(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)
	shrinkReopenBackoff(t)
	deviceWatchOverride.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { deviceWatchOverride.Store(0) })

	var stamp atomic.Value
	var reads atomic.Int32
	stamp.Store("uid@16000")
	stampFn := func(string) (string, bool) {
		reads.Add(1)
		current, ok := stamp.Load().(string)

		return current, ok
	}

	opened := make(chan *observedSink, 4)
	var opens atomic.Int32
	start := func(context.Context) (io.WriteCloser, error) {
		n := opens.Add(1)
		if n == 2 || n == 3 { // the first two reopen attempts hit a mid-flap device
			return nil, errors.New("audiotoolbox: device not found")
		}
		sink := newObservedSink()
		opened <- sink

		return sink, nil
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger(), WithConfigStamp(stampFn))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	first := awaitObservedSink(t, opened, "initial device sink did not open")
	assertObservedWrite(t, first, 2*pacing.samples, "initial device sink received no PCM quantum")

	testkit.Eventually(t, 5*time.Second, func() bool {
		return reads.Load() >= 2
	}, "watcher never sampled the fingerprint")
	stamp.Store("uid@48000")

	second := awaitObservedSink(t, opened, "route did not survive the failed reopen attempts")
	assertObservedWrite(t, second, 2*pacing.samples, "self-healed device sink received no PCM quantum")
	if opens.Load() < 4 {
		t.Errorf("opens = %d, want the two failed attempts retried through", opens.Load())
	}
}

// erroringSink accepts a bounded number of writes, then fails permanently —
// the shape of an audiotoolbox process dying mid-stream.
type erroringSink struct {
	mu      sync.Mutex
	healthy int
}

func (s *erroringSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthy <= 0 {
		return 0, errors.New("write to dead encoder")
	}
	s.healthy--

	return len(p), nil
}

func (*erroringSink) Close() error { return nil }

// TestDeviceSinkReopensAfterWriteError: a device sink whose encoder dies with
// a write error is reopened rather than retired — on macOS an audiotoolbox
// death is environmental (device vanished, sample rate switched), and the old
// fail-once behavior left the route permanently mute while the session kept
// reporting healthy.
func TestDeviceSinkReopensAfterWriteError(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)
	shrinkReopenBackoff(t)

	opened := make(chan *observedSink, 4)
	var opens atomic.Int32
	start := func(context.Context) (io.WriteCloser, error) {
		if opens.Add(1) == 1 {
			return &erroringSink{healthy: 3}, nil
		}
		sink := newObservedSink()
		opened <- sink

		return sink, nil
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}

	healed := awaitObservedSink(t, opened, "device sink was not reopened after its write error")
	assertObservedWrite(t, healed, 2*pacing.samples, "reopened device sink received no PCM quantum")
}

// TestRecoverEncoderStillFailsNetworkJobs: the self-heal is scoped to device
// sinks; a network push that errors keeps failing fast so the operator sees it.
func TestRecoverEncoderStillFailsNetworkJobs(t *testing.T) {
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	start := func(context.Context) (io.WriteCloser, error) { return devNullSink{}, nil }

	_, verdict := r.recoverEncoder(
		t.Context(), errors.New("rtmp handshake failed"), "job", "pair", false, start, &encoderBinding{},
	)
	if verdict != encoderFailed {
		t.Fatalf("verdict = %v, want encoderFailed for a network job", verdict)
	}
}

// blockingSink accepts writes until wedged, then blocks every Write until the
// sink is closed — the shape of an alive audiotoolbox process whose queue
// stopped draining.
type blockingSink struct {
	closed   chan struct{}
	healthy  int
	unblock  sync.Once
	mu       sync.Mutex
	blocking atomic.Bool
}

func newBlockingSink(healthy int) *blockingSink {
	return &blockingSink{closed: make(chan struct{}), healthy: healthy}
}

func (s *blockingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	remaining := s.healthy
	if remaining > 0 {
		s.healthy--
	}
	s.mu.Unlock()
	if remaining > 0 {
		return len(p), nil
	}

	s.blocking.Store(true)
	<-s.closed // wedged: only Close releases the writer

	return 0, errors.New("sink severed while wedged")
}

func (s *blockingSink) Close() error {
	s.unblock.Do(func() { close(s.closed) })

	return nil
}

// shrinkStallTimeout makes stall detection immediate for tests.
func shrinkStallTimeout(t *testing.T) {
	t.Helper()
	deviceWriteStallOverride.Store(int64(30 * time.Millisecond))
	t.Cleanup(func() { deviceWriteStallOverride.Store(0) })
}

// TestDeviceSinkRecoversFromAWedgedEncoder: an alive-but-stalled sink is the
// silence the reopen path cannot see on its own — the guard must sever it and
// the job must rebuild a working sink.
func TestDeviceSinkRecoversFromAWedgedEncoder(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)
	shrinkStallTimeout(t)
	shrinkReopenBackoff(t)

	opened := make(chan *observedSink, 4)
	var opens atomic.Int32
	start := func(context.Context) (io.WriteCloser, error) {
		if opens.Add(1) == 1 {
			return newBlockingSink(2), nil // wedges after two quanta
		}
		sink := newObservedSink()
		opened <- sink

		return sink, nil
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob("push", "call", "en", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("launch: %v", err)
	}

	healed := awaitObservedSink(t, opened, "wedged device sink was never severed and rebuilt")
	assertObservedWrite(t, healed, 2*pacing.samples, "rebuilt device sink received no PCM quantum")
}

// TestDevicePushIsRelaunchableOnAVoiceQueue: replacing a call device push
// (stop old job, start new one on the SAME VoiceQueue) must succeed — the
// one-shot consumer gate refused every re-push with "not ready" forever.
func TestDevicePushIsRelaunchableOnAVoiceQueue(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)

	opened := make(chan *observedSink, 4)
	start := func(context.Context) (io.WriteCloser, error) {
		sink := newObservedSink()
		opened <- sink

		return sink, nil
	}

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "de", pipeline.NewVoiceQueue(0), WithFeedQuantum(5*time.Millisecond))

	if err := r.launchMixFedJob("push", "call", "de", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	first := awaitObservedSink(t, opened, "first push sink did not open")
	assertObservedWrite(t, first, 2*pacing.samples, "first push received no PCM")

	// The relaunch path: launch with the same job identity stops the old job
	// and must be admitted onto the same queue.
	if err := r.launchMixFedJob("push", "call", "de", "device://audio/3?label=Prukka+Microphone", start); err != nil {
		t.Fatalf("re-push refused: %v", err)
	}
	second := awaitObservedSink(t, opened, "replacement push sink did not open")
	assertObservedWrite(t, second, 2*pacing.samples, "replacement push received no PCM")
}
