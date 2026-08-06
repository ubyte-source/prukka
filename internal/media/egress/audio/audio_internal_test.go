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
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"

	"github.com/ubyte-source/prukka/internal/testkit"
)

type countingCloser struct{ closes int }

const chunk = DefaultFeedQuantum

var chunkTickSamples = pipeline.SamplesInQuantum(chunk)

func (*countingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *countingCloser) Close() error              { c.closes++; return nil }

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
		f := &feeder{out: sink, mixer: cursor, pacing: defaultFeedConfig(), sink: sinkEncoder}
		feedDone <- f.ticks(t.Context(), ticks)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- template.WaitPlayout(t.Context()) }()

	ticks <- time.Time{}
	<-sink.wrote
	if sink.writer.bytes != chunkTickSamples*2 || !sink.writer.nonZero {
		t.Fatalf("final write = %d bytes, non-zero %v", sink.writer.bytes, sink.writer.nonZero)
	}

	// The next pull observes EOF.
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
		t.Fatalf("feeder.ticks: %v", err)
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

	r.live.mu.RLock()
	english := r.live.pairs[pairID{slug: "call", lang: "en"}].pacing
	italian := r.live.pairs[pairID{slug: "call", lang: "it"}].pacing
	r.live.mu.RUnlock()

	if english.quantum != 20*time.Millisecond || english.samples != core.SampleRate/50 {
		t.Fatalf("English feed = %v/%d samples, want 20ms/%d", english.quantum, english.samples, core.SampleRate/50)
	}
	if italian.quantum != DefaultFeedQuantum || italian.samples != chunkTickSamples {
		t.Fatalf("Italian default feed = %v/%d samples, want %v/%d",
			italian.quantum, italian.samples, DefaultFeedQuantum, chunkTickSamples)
	}

	r.Drop("call")
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	if remaining := len(r.live.pairs); remaining != 0 {
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
	cursor := pipeline.NewMixer(bed, voice, -15).Cursor()
	t.Cleanup(cursor.ReleasePlayout)

	ticks := make(chan time.Time, 2)
	ticks <- time.Time{}
	ticks <- time.Time{}
	w := &recordingWriter{}
	observedPeak := 0
	f := &feeder{out: writeOnly{w},
		mixer:     cursor,
		pacing:    pacing,
		sink:      sinkEncoder,
		observers: []func(core.PCM){func(pcm core.PCM) { observedPeak = pipeline.PeakS16(pcm.Data) }},
	}
	if err := f.pace(t.Context(), ticks); err != nil {
		t.Fatalf("pace = %v", err)
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
	cursor := pipeline.NewMixer(bed, voice, -15).Cursor()
	t.Cleanup(cursor.ReleasePlayout)

	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	observed := false
	f := &feeder{out: writeOnly{shortWriter{}},
		mixer:     cursor,
		pacing:    pacing,
		sink:      sinkEncoder,
		observers: []func(core.PCM){func(core.PCM) { observed = true }},
	}
	err := f.pace(t.Context(), ticks)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("pace short write = %v, want io.ErrShortWrite", err)
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

	f := &feeder{out: writeOnly{w}, mixer: idleMixer().Cursor(), pacing: pacing, sink: sinkDevice}
	if err := f.pace(ctx, ticks); err != nil {
		t.Fatalf("pace = %v", err)
	}
	if w.writer.bytes != pacing.samples*2 {
		t.Fatalf("device fill = %d bytes, want %d", w.writer.bytes, pacing.samples*2)
	}
	if w.writer.nonZero {
		t.Fatal("idle device fill was not silence")
	}
}

func TestPaceFillsIdleTicksForDeviceSinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*chunk)
	defer cancel()

	w := &recordingWriter{}
	f := &feeder{out: writeOnly{w}, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkDevice}
	if err := pace(ctx, f); err != nil {
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
	f := &feeder{out: writeOnly{w}, mixer: idleMixer().Cursor(), pacing: defaultFeedConfig(), sink: sinkEncoder}
	if err := pace(ctx, f); err != nil {
		t.Fatalf("pace = %v, want nil", err)
	}
	if w.bytes != 0 {
		t.Fatalf("anchored feed wrote %d bytes before the mixer anchor", w.bytes)
	}
}

func pushID(slug string, lang core.Lang, target string) jobID {
	return jobID{kind: kindPush, pair: pairID{slug: slug, lang: lang}, target: target}
}

func hlsID(pair pairID) jobID {
	return jobID{kind: kindHLS, pair: pair}
}

func TestJobLabelSeparatesTargetsWithoutLeakingThem(t *testing.T) {
	t.Parallel()

	first := pushID("call", "en", "device://audio/1").String()
	second := pushID("call", "en", "device://video/prukka").String()
	if first == second {
		t.Fatal("distinct push targets share a job label")
	}
	if strings.Contains(first, "device://") || strings.Contains(second, "device://") {
		t.Fatalf("job labels expose target URLs: %q %q", first, second)
	}
	if got := pushID("call", "en", "device://audio/1").String(); got != first {
		t.Fatalf("same target label = %q, want stable %q", got, first)
	}
	if got := (jobID{kind: kindHLS, pair: pairID{slug: "call", lang: "en"}}).String(); got != "hls:call/en" {
		t.Fatalf("targetless job label = %q, want %q", got, "hls:call/en")
	}
}

func TestPushTargetLimitIsBoundedPerPair(t *testing.T) {
	t.Parallel()

	state := &liveState{jobs: map[jobID]job{}, routes: map[jobID]session.SubsMode{}}
	for i := range maxPushTargetsPerPair {
		state.jobs[pushID("call", "en", fmt.Sprintf("target-%d", i))] = job{}
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.admitPushTargetLocked(pushID("call", "en", "overflow")) {
		t.Fatal("push target limit accepted an additional target")
	}
	if !state.admitPushTargetLocked(pushID("call", "it", "independent")) {
		t.Fatal("one language consumed another language's target budget")
	}
	if !state.admitPushTargetLocked(pushID("call", "en", "target-0")) {
		t.Fatal("restarting an existing target was counted as a new one")
	}
}

func TestPushTargetBoundIsOneDecisionOverJobsAndRoutes(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))

	// All but one of the pair's budget as live encoder jobs.
	for i := range maxPushTargetsPerPair - 1 {
		target := fmt.Sprintf("rtmp://127.0.0.1:1/live/job-%d", i)
		start := func(context.Context) (io.WriteCloser, error) { return devNullSink{}, nil }
		if err := r.launchMixFedJob(pushID("cast", "en", target), start); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}

	// No supervisor: every Push lands as a not-ready intent.
	for i := range 2 * maxPushTargetsPerPair {
		target := fmt.Sprintf("rtmp://127.0.0.1:1/live/route-%d", i)
		err := r.Push("cast", "en", target, "off")
		accepted := err == nil || errors.Is(err, core.ErrNotReady)

		remembered := routeRemembered(r, pushID("cast", "en", target))
		if accepted != remembered {
			t.Fatalf("Push(%q) = %v with remembered=%v; an accepted push must always be remembered",
				target, err, remembered)
		}
	}

	if got := countPushTargets(r, "cast", "en"); got != maxPushTargetsPerPair {
		t.Fatalf("pair holds %d push targets, want the bound of %d", got, maxPushTargetsPerPair)
	}
}

func routeRemembered(r *Registry, id jobID) bool {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	_, ok := r.live.routes[id]

	return ok
}

func jobRunning(r *Registry, id jobID) bool {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	_, ok := r.live.jobs[id]

	return ok
}

func pairRegistered(r *Registry, pair pairID) bool {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	_, ok := r.live.pairs[pair]

	return ok
}

func routeCount(r *Registry) int {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	return len(r.live.routes)
}

func jobCount(r *Registry) int {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	return len(r.live.jobs)
}

// gateState reports whether one session's gate is present and, if so, whether
// it has closed admission.
func gateState(r *Registry, slug string) (present, finishing bool) {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	g, present := r.live.gates[slug]

	return present, present && g.finishing
}

// countPushTargets counts the distinct push targets one pair holds.
func countPushTargets(r *Registry, slug string, lang core.Lang) int {
	r.live.mu.RLock()
	defer r.live.mu.RUnlock()

	pair := pairID{slug: slug, lang: lang}
	seen := map[jobID]struct{}{}
	for id := range r.live.jobs {
		if id.kind == kindPush && id.pair == pair {
			seen[id] = struct{}{}
		}
	}
	for id := range r.live.routes {
		if id.kind == kindPush && id.pair == pair {
			seen[id] = struct{}{}
		}
	}

	return len(seen)
}

func TestCreateAndResetAcrossSessionsNeverCouple(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())

	var storm sync.WaitGroup
	for _, slug := range []string{"alpha", "beta"} {
		storm.Go(func() {
			for range 200 {
				r.Create(slug, "en", idleMixer())
				r.Reset(slug)
			}
		})
	}
	storm.Wait()

	r.Create("alpha", "en", idleMixer())
	if alive, _ := gateState(r, "alpha"); !alive {
		t.Fatal("registry unusable after concurrent Create/Reset storm")
	}
}

func TestDropWaitsForEncoderTeardown(t *testing.T) {
	t.Parallel()

	r := &Registry{}
	r.live.start()
	canceled := make(chan struct{})
	done := make(chan struct{})
	r.live.mu.Lock()
	r.live.jobs[hlsID(pairID{slug: "demo", lang: "en"})] = job{
		cancel: func() { close(canceled) },
		done:   done,
	}
	r.live.mu.Unlock()

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

func TestSessionTeardownSparesPrefixSharingSiblings(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	r.Create("cast", "en", idleMixer())
	r.Create("cast-2", "en", idleMixer())

	ownJob := hlsID(pairID{slug: "cast", lang: "en"})
	siblingJob := hlsID(pairID{slug: "cast-2", lang: "en"})
	ownRoute := pushID("cast", "en", "rtmp://127.0.0.1:1/live/own")
	siblingRoute := pushID("cast-2", "en", "rtmp://127.0.0.1:1/live/sibling")
	siblingCanceled := false
	r.live.mu.Lock()
	r.live.jobs[ownJob] = job{cancel: func() {}}
	r.live.jobs[siblingJob] = job{cancel: func() { siblingCanceled = true }}
	r.live.routes[ownRoute] = session.SubsOff
	r.live.routes[siblingRoute] = session.SubsOff
	r.live.mu.Unlock()

	r.Reset("cast") // a lane restart: pairs and jobs go, routes stay
	if !routeRemembered(r, siblingRoute) {
		t.Fatal(`Reset("cast") forgot the remembered route of "cast-2"`)
	}

	r.Drop("cast") // the session ends: its own routes go too
	siblingPair := pairRegistered(r, pairID{slug: "cast-2", lang: "en"})
	siblingJobKept := jobRunning(r, siblingJob)
	siblingRouteKept := routeRemembered(r, siblingRoute)
	ownPair := pairRegistered(r, pairID{slug: "cast", lang: "en"})
	ownJobKept := jobRunning(r, ownJob)
	ownRouteKept := routeRemembered(r, ownRoute)

	if !siblingPair || !siblingJobKept || !siblingRouteKept || siblingCanceled {
		t.Errorf("sibling after tearing down \"cast\": pair=%v job=%v route=%v canceled=%v, want all kept",
			siblingPair, siblingJobKept, siblingRouteKept, siblingCanceled)
	}
	if ownPair || ownJobKept || ownRouteKept {
		t.Errorf("dropped session kept pair=%v job=%v route=%v, want every one gone",
			ownPair, ownJobKept, ownRouteKept)
	}
}

// parkedVideoSource parks the first VideoPlaylist lookup, which dispatchPush
// makes for every network target, holding a Push RPC inside its dispatch.
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

	if kept := routeCount(r); kept != 0 {
		t.Fatalf("routes after Drop = %d, want 0: the push re-recorded its intent after the sweep", kept)
	}
}

func TestPushBeforeTheLanePublishesItsPairRemembersTheIntent(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())

	if err := r.Push("cast", "en", "rtmp://127.0.0.1:1/live/key", "off"); !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("push before the lane is up = %v, want ErrNotReady", err)
	}

	if kept := routeCount(r); kept != 1 {
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
	r.live.mu.Lock()
	r.live.jobs[hlsID(pairID{slug: "demo", lang: "en"})] = job{feed: jobMixFed, done: jobDone, cancel: func() {}}
	r.live.mu.Unlock()

	waited := make(chan error, 1)
	go func() { waited <- r.WaitPlayout(t.Context(), "demo") }()

	testkit.Eventually(t, time.Second, func() bool {
		_, finishing := gateState(r, "demo")

		return finishing
	}, "WaitPlayout did not close session admission")

	started := false
	err := r.launchMixFedJob(hlsID(pairID{slug: "demo", lang: "en"}), func(context.Context) (io.WriteCloser, error) {
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
	demo := hlsID(pairID{slug: "demo", lang: "en"})
	r.live.mu.Lock()
	r.live.jobs[demo] = job{feed: jobMixFed, done: make(chan struct{}), cancel: func() {}}
	r.live.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.WaitPlayout(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitPlayout = %v, want context.Canceled", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

type devNullSink struct{}

func (s devNullSink) Write(p []byte) (int, error) { return len(p), nil }

func (s devNullSink) Close() error { return nil }

// observedSink exposes the writes a reopened encoder receives.
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

func TestLaunchReopensDeviceSinkOnConfigurationChange(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)

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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithConfigStamp(stampFn), withDeviceTiming(deviceTiming{watch: 5 * time.Millisecond}))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
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

func TestFeedWatchedStaysInertWithoutAFingerprint(t *testing.T) {
	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithConfigStamp(func(string) (string, bool) { return "", false }))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := r.feedWatched(ctx, &feeder{
		out:    devNullSink{},
		mixer:  idleMixer().Cursor(),
		target: "device://audio/3?label=X",
		pacing: feedConfig{quantum: time.Millisecond, samples: 16},
		sink:   sinkDevice,
	})
	if err != nil {
		t.Fatalf("inert feedWatched = %v", err)
	}
}

func TestFeedWatchedSignalsReconfiguration(t *testing.T) {
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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithConfigStamp(stampFn), withDeviceTiming(deviceTiming{watch: 5 * time.Millisecond}))
	err := r.feedWatched(t.Context(), &feeder{
		out:    devNullSink{},
		mixer:  idleMixer().Cursor(),
		target: "device://audio/3?label=X",
		pacing: feedConfig{quantum: 5 * time.Millisecond, samples: 80},
		sink:   sinkDevice,
	})
	if !errors.Is(err, errDeviceReconfigured) {
		t.Fatalf("feedWatched = %v, want errDeviceReconfigured", err)
	}
}

func TestFeedWatchedAcquiresPendingLabeledBaseline(t *testing.T) {
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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithConfigStamp(stampFn), withDeviceTiming(deviceTiming{watch: 5 * time.Millisecond}))
	err := r.feedWatched(t.Context(), &feeder{
		out:    devNullSink{},
		mixer:  idleMixer().Cursor(),
		target: "device://audio/3?label=Prukka+Microphone",
		pacing: feedConfig{quantum: 5 * time.Millisecond, samples: 80},
		sink:   sinkDevice,
	})
	if !errors.Is(err, errDeviceReconfigured) {
		t.Fatalf("pending-baseline feedWatched = %v, want errDeviceReconfigured", err)
	}
	if got := reads.Load(); got < 4 {
		t.Fatalf("fingerprint reads = %d, want pending, baseline and changed samples", got)
	}
}

func countingStart(opened chan struct{}) func(context.Context) (io.WriteCloser, error) {
	return func(context.Context) (io.WriteCloser, error) {
		opened <- struct{}{}

		return devNullSink{}, nil
	}
}

func TestEncoderJobReattachesAfterPairRebuild(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bedA, voiceA := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", pipeline.NewMixer(bedA, voiceA, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-opened

	// The lane dies: the cursor drains to EOF before the restart publishes
	// fresh mixers.
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

func TestEncoderJobEndsQuietlyWhenSessionFinishes(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("cast", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-opened

	if _, _, _, ok := r.finishSnapshot("cast"); !ok {
		t.Fatal("finishSnapshot refused the session")
	}
	bed.Finish()
	voice.Finish()

	testkit.Eventually(t, 5*time.Second, func() bool {
		return jobCount(r) == 0
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

// Admission must be evaluated INSIDE the snapshot's lock window, and only
// this ordering — the job parked in the re-attach poll BEFORE the session
// finishes — lets the race detector see it.
func TestSessionFinishesUnderAJobAlreadyPollingForItsRebuiltPair(t *testing.T) {
	t.Parallel()

	sink := &drainedSink{}
	start := func(context.Context) (io.WriteCloser, error) { return sink, nil }

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("cast")
	r.Create("cast", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("cast", "en", "rtmp://relay.example/live"), start); err != nil {
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
		return jobCount(r) == 0
	}, "the polling job outlived the session that finished under it")
}

func TestPushReplacementDoesNotDeadlockAReattachingJob(t *testing.T) {
	t.Parallel()

	opened := make(chan struct{}, 4)
	start := countingStart(opened)

	bed, voice := pipeline.NewTrack(), pipeline.NewTrack()
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", pipeline.NewMixer(bed, voice, -15), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	<-opened

	// Drain the first job to EOF so it parks in the re-attach wait.
	bed.Finish()
	voice.Finish()
	time.Sleep(100 * time.Millisecond)

	replaced := make(chan error, 1)
	go func() {
		replaced <- r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start)
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

func TestPushRoutesSurviveLaneResetAndRelaunch(t *testing.T) {
	if runtime.GOOS == hostos.Windows {
		// The sink start execs a POSIX shell script Windows cannot run.
		t.Skip("fake ffmpeg helper is not a runnable Windows executable")
	}
	t.Parallel()

	fake := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, fake, "#!/bin/sh\nwhile read line; do :; done\n", 0o700)

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.SetSupervisor(ffmpeg.NewSupervisor(fake, discardLogger()))
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))

	// A device:// target would eagerly open a real WASAPI endpoint on Windows.
	target := "rtmp://127.0.0.1:1/live/push-reset"
	if err := r.Push("call", "en", target, "off"); err != nil {
		t.Fatalf("push: %v", err)
	}
	id := pushID("call", "en", target)

	// The lane dies and clears its tree; the route intent survives.
	r.Reset("call")
	jobAlive, routeKept := jobRunning(r, id), routeRemembered(r, id)
	if jobAlive || !routeKept {
		t.Fatalf("after Reset: job=%v route=%v, want job gone and route kept", jobAlive, routeKept)
	}

	// The restarted lane re-registers the pair: the route relaunches.
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	testkit.Eventually(t, 5*time.Second, func() bool {
		return jobRunning(r, id)
	}, "route did not relaunch on the rebuilt pair")

	// Drop ends the session for good: the intent must not survive.
	r.Drop("call")
	if routeRemembered(r, id) {
		t.Fatal("Drop kept the push route")
	}
}

// pace drives one feeder on a real-time ticker.
func pace(ctx context.Context, f *feeder) error {
	ticker := time.NewTicker(f.pacing.quantum)
	defer ticker.Stop()

	return f.pace(ctx, ticker.C)
}

type writeOnly struct {
	io.Writer
}

func (writeOnly) Close() error { return nil }

func TestDeviceSinkSelfHealsAcrossFailedReopens(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)

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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(), WithConfigStamp(stampFn),
		withDeviceTiming(deviceTiming{watch: 5 * time.Millisecond, retry: time.Millisecond}))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	target := pushID("call", "en", "device://audio/3?label=Prukka+Microphone")
	if err := r.launchMixFedJob(target, start); err != nil {
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

// erroringSink fails permanently after a bounded number of writes.
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

func TestDeviceSinkReopensAfterWriteError(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)

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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		withDeviceTiming(deviceTiming{retry: time.Millisecond}))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("launch: %v", err)
	}

	healed := awaitObservedSink(t, opened, "device sink was not reopened after its write error")
	assertObservedWrite(t, healed, 2*pacing.samples, "reopened device sink received no PCM quantum")
}

func TestRecoverEncoderStillFailsNetworkJobs(t *testing.T) {
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	start := func(context.Context) (io.WriteCloser, error) { return devNullSink{}, nil }

	_, verdict := r.recoverEncoder(
		t.Context(), errors.New("rtmp handshake failed"),
		pushID("call", "en", "rtmp://relay.example/live"), sinkEncoder, start, &encoderBinding{},
	)
	if verdict != encoderFailed {
		t.Fatalf("verdict = %v, want encoderFailed for a network job", verdict)
	}
}

// blockingSink accepts writes until wedged, then blocks every Write until the
// sink is closed.
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

func TestDeviceSinkRecoversFromAWedgedEncoder(t *testing.T) {
	pacing := makeFeedConfig(5 * time.Millisecond)

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

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		withDeviceTiming(deviceTiming{stall: deviceStallBudget, retry: time.Millisecond}))
	defer r.Drop("call")
	r.Create("call", "en", idleMixer(), WithFeedQuantum(5*time.Millisecond))
	if err := r.launchMixFedJob(pushID("call", "en", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("launch: %v", err)
	}

	healed := awaitObservedSink(t, opened, "wedged device sink was never severed and rebuilt")
	assertObservedWrite(t, healed, 2*pacing.samples, "rebuilt device sink received no PCM quantum")
}

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

	if err := r.launchMixFedJob(pushID("call", "de", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	first := awaitObservedSink(t, opened, "first push sink did not open")
	assertObservedWrite(t, first, 2*pacing.samples, "first push received no PCM")

	// The same job identity stops the old job and must be admitted again.
	if err := r.launchMixFedJob(pushID("call", "de", "device://audio/3?label=Prukka+Microphone"), start); err != nil {
		t.Fatalf("re-push refused: %v", err)
	}
	second := awaitObservedSink(t, opened, "replacement push sink did not open")
	assertObservedWrite(t, second, 2*pacing.samples, "replacement push received no PCM")
}
