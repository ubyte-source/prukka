package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// targetQueue decouples transcription from provider latency: a slow target
// never stalls the read pump beyond this many pending segments.
const targetQueue = 4

// Sink consumes final translated segments for one target language.
type Sink interface {
	Append(seg *core.TranslatedSegment)
}

// Metrics records end-to-end timings; nil disables recording.
type Metrics interface {
	E2ELatency(kind string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) E2ELatency(string, time.Duration) {}

// Stream identifies the audio a lane processes and how it is timed.
type Stream struct {
	Session string
	Track   string
	// Source hints the spoken language; core.LangAuto lets the transcriber
	// detect it.
	Source core.Lang
	// Delay is the per-session delay D added to output timing.
	Delay time.Duration
}

// Providers are the transcription and translation ports the engine drives.
type Providers struct {
	Transcriber Transcriber
	Translator  Translator
}

// Dub is the optional voice stage: streaming synthesis laid onto per-language
// timelines. A single configured voice is used for every take.
type Dub struct {
	Synthesizer Synthesizer
	Tracks      map[core.Lang]*pipeline.Track
	// Bed receives the original source audio, delayed by D, so the mixer can
	// keep it under the dubbed voice. Optional.
	Bed   *pipeline.Track
	Voice core.Voice
}

// Output routes the engine's results: a caption sink per target language and,
// when Dub is set, the voice stage.
type Output struct {
	Sinks map[core.Lang]Sink
	Dub   *Dub
}

// Config wires one streaming lane. Metrics is optional.
type Config struct {
	Providers Providers
	Output    Output
	Metrics   Metrics
	Stream    Stream
}

// Engine runs one session's streaming pipeline: transcribe, translate and
// synthesize with the stages overlapping in time.
type Engine struct {
	log       *slog.Logger
	metrics   Metrics
	providers Providers
	output    Output
	stream    Stream
}

// New wires an engine; call Run to start it.
func New(cfg *Config, log *slog.Logger) *Engine {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}

	return &Engine{
		log:       log,
		metrics:   metrics,
		providers: cfg.Providers,
		output:    cfg.Output,
		stream:    cfg.Stream,
	}
}

// timedSegment carries the instant a segment was finalized, so end-to-end
// latency measures from speech end.
type timedSegment struct {
	at  time.Time
	seg Segment
}

// frameOwner closes sources that expose lifecycle control without extending
// the Frames port. sync.Once also makes cancellation and final cleanup agree
// on a single owner.
type frameOwner struct {
	closer   io.Closer
	closeErr error
	once     sync.Once
}

func (o *frameOwner) closeForCancel() {
	o.once.Do(func() {
		if o.closer != nil {
			o.closeErr = o.closer.Close()
		}
	})
}

func (o *frameOwner) close() error {
	o.closeForCancel()

	return o.closeErr
}

// Run drives the pipeline until the source ends or ctx is canceled. Transcript
// events feed one serial worker per target so per-language order is preserved
// while languages progress independently. Run owns frames for its duration and
// closes it when the concrete source implements io.Closer.
func (e *Engine) Run(ctx context.Context, frames core.Frames) (runErr error) {
	owned := frameOwner{}
	if closer, ok := frames.(io.Closer); ok {
		owned.closer = closer
	}
	defer func() {
		if err := owned.close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close source frames: %w", err))
		}
	}()

	if e.stream.Source != core.LangAuto {
		if err := e.validateTranslationTargets(e.stream.Source); err != nil {
			return err
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	group, gctx := errgroup.WithContext(runCtx)
	stopCancelClose := context.AfterFunc(gctx, owned.closeForCancel)
	defer stopCancelClose()

	transcription, err := e.providers.Transcriber.Open(gctx, e.stream.Source)
	if err != nil {
		return fmt.Errorf("open transcription: %w", err)
	}

	e.configurePlayout()

	clock := &sourceClock{}
	workers := e.startTargets(gctx, group)

	group.Go(func() error {
		defer closeTargets(workers)

		return e.consume(gctx, transcription, clock, workers)
	})

	group.Go(func() error {
		return e.pump(gctx, frames, transcription, clock)
	})

	err = group.Wait()
	if closeErr := transcription.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close transcription: %w", closeErr))
	}
	if err == nil {
		// The source ended cleanly: close the dub tracks so their buffered,
		// delayed tail becomes drainable instead of stalling the mixer on the
		// cushion that waits for more source audio.
		e.finishDub()
	}

	return err
}

// finishDub marks the bed and every voice track complete so a finite lane's
// delayed dubbed audio drains in full after the source ends.
func (e *Engine) finishDub() {
	if e.output.Dub == nil {
		return
	}

	if e.output.Dub.Bed != nil {
		e.output.Dub.Bed.Finish()
	}

	for _, track := range e.output.Dub.Tracks {
		track.Finish()
	}
}

// configurePlayout maps every dubbed timeline onto real time before pumping.
func (e *Engine) configurePlayout() {
	if e.output.Dub == nil {
		return
	}

	if e.output.Dub.Bed != nil {
		e.output.Dub.Bed.ConfigurePlayout(e.stream.Delay)
	}

	for _, track := range e.output.Dub.Tracks {
		track.ConfigurePlayout(e.stream.Delay)
	}
}

// startTargets launches one serial worker per target language.
func (e *Engine) startTargets(ctx context.Context, group *errgroup.Group) map[core.Lang]chan timedSegment {
	workers := make(map[core.Lang]chan timedSegment, len(e.output.Sinks))

	for target, sink := range e.output.Sinks {
		in := make(chan timedSegment, targetQueue)
		workers[target] = in

		group.Go(func() error {
			return e.serveTarget(ctx, target, sink, in)
		})
	}

	return workers
}

func closeTargets(workers map[core.Lang]chan timedSegment) {
	for _, in := range workers {
		close(in)
	}
}

// pump frames the source into the transcriber and lays the original audio on
// the bed at PTS+D; the source clock advances with each frame.
func (e *Engine) pump(
	ctx context.Context, frames core.Frames, transcription Transcription, clock *sourceClock,
) error {
	bed := e.bed()

	for {
		frame, err := frames.Next(ctx)
		if errors.Is(err, io.EOF) {
			return transcription.CloseSend()
		}

		if err != nil {
			return fmt.Errorf("source: %w", err)
		}

		if bed != nil {
			bed.Append(frame.PTS+e.stream.Delay, frame.Data)
		}

		clock.set(frame.PTS)

		if pushErr := transcription.Push(frame); pushErr != nil {
			return pushErr
		}
	}
}

func (e *Engine) bed() *pipeline.Track {
	if e.output.Dub == nil {
		return nil
	}

	return e.output.Dub.Bed
}

// consume runs the wait-k commit policy over transcript updates: partials feed
// local agreement, committed clauses fan out to every target worker in order.
// A finals-only adapter commits each segment whole; a streaming one commits
// clause by clause as speech stabilizes.
func (e *Engine) consume(
	ctx context.Context, transcription Transcription, clock *sourceClock, workers map[core.Lang]chan timedSegment,
) error {
	var commit committer

	segStart := time.Duration(0)
	lang := e.stream.Source

	events := transcription.Events()
eventLoop:
	for {
		var update Transcript
		select {
		case next, ok := <-events:
			if !ok {
				break eventLoop
			}
			update = next
		case <-ctx.Done():
			return ctx.Err()
		}

		if update.Lang != "" {
			lang = update.Lang
		}

		if err := e.emitValidated(
			ctx, workers, commit.commit(update), lang, &segStart, clock.now(),
		); err != nil {
			return err
		}
	}
	if terminalErr := transcription.Err(); terminalErr != nil {
		return fmt.Errorf("transcription: %w", terminalErr)
	}

	// The stream can close mid-utterance with no Final (audio EOF); release the
	// held tail so its last clause is not dropped.
	return e.emitValidated(ctx, workers, commit.flushHold(), lang, &segStart, clock.now())
}

func (e *Engine) emitValidated(
	ctx context.Context, workers map[core.Lang]chan timedSegment,
	clauses []string, lang core.Lang, segStart *time.Duration, now time.Duration,
) error {
	if len(clauses) != 0 {
		if err := e.validateTranslationTargets(lang); err != nil {
			return err
		}
	}

	return e.emit(ctx, workers, clauses, lang, segStart, now)
}

// emit spans a batch of committed clauses across the source interval since the
// last commit, splitting it by clause length so each caption gets a non-zero
// duration, and fans every clause to the target workers in order.
func (e *Engine) emit(
	ctx context.Context, workers map[core.Lang]chan timedSegment,
	clauses []string, lang core.Lang, segStart *time.Duration, now time.Duration,
) error {
	total := 0
	for _, clause := range clauses {
		total += len(clause)
	}

	if total == 0 {
		return nil
	}

	span := max(now-*segStart, 0)
	base := *segStart
	acc := 0

	for _, clause := range clauses {
		acc += len(clause)
		end := base + span*time.Duration(acc)/time.Duration(total)
		segment := timedSegment{
			at:  time.Now(),
			seg: Segment{Text: clause, Lang: lang, Span: [2]time.Duration{*segStart, end}},
		}
		*segStart = end

		e.log.Debug("clause committed", "session", e.stream.Session, "lang", lang)

		if !fanout(ctx, workers, &segment) {
			return ctx.Err()
		}
	}

	return nil
}

func (e *Engine) validateTranslationTargets(source core.Lang) error {
	for _, target := range slices.Sorted(maps.Keys(e.output.Sinks)) {
		if sameBase(source, target) {
			continue
		}
		if source == core.LangAuto {
			return fmt.Errorf("translation source language was not detected for target %s", target)
		}
		if !e.providers.Translator.Supports(source, target) {
			return fmt.Errorf("translation model unavailable for %s to %s", source, target)
		}
	}

	return nil
}

// fanout delivers one segment to every target worker, honoring cancellation.
func fanout(ctx context.Context, workers map[core.Lang]chan timedSegment, segment *timedSegment) bool {
	for _, in := range workers {
		select {
		case in <- *segment:
		case <-ctx.Done():
			return false
		}
	}

	return true
}

// voiceJob is a captioned take handed from the translate stage to the voice
// stage; endpointAt is when its source speech ended, for the voice metric.
type voiceJob struct {
	endpointAt time.Time
	seg        *core.TranslatedSegment
}

// serveTarget processes one language's segments. Translation and captioning run
// ahead of synthesis: when the target is dubbed, a voice stage drains
// synthesized speech in parallel so a slow take never stalls the next caption.
func (e *Engine) serveTarget(ctx context.Context, target core.Lang, sink Sink, in <-chan timedSegment) error {
	track := e.track(target)
	if track == nil {
		return e.captionOnly(ctx, target, sink, in)
	}

	targetCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan voiceJob, targetQueue)
	stageErr := make(chan error, 1)
	go func() { stageErr <- e.translateStage(targetCtx, target, sink, in, jobs) }()

	var voiceErr error
	for job := range jobs {
		if err := e.speak(targetCtx, target, track, job); err != nil {
			voiceErr = err

			break
		}
	}
	cancel()
	translateErr := <-stageErr
	if voiceErr != nil {
		return voiceErr
	}

	return translateErr
}

// captionOnly translates and captions each segment serially, with no voice.
func (e *Engine) captionOnly(ctx context.Context, target core.Lang, sink Sink, in <-chan timedSegment) error {
	for segment := range in {
		if _, err := e.caption(ctx, target, sink, &segment); err != nil {
			return err
		}
	}

	return nil
}

// translateStage translates and captions each segment in order and enqueues the
// take for synthesis, closing jobs when the source ends so the voice stage
// drains and returns.
func (e *Engine) translateStage(
	ctx context.Context, target core.Lang, sink Sink, in <-chan timedSegment, jobs chan<- voiceJob,
) error {
	defer close(jobs)

	for {
		var segment timedSegment
		select {
		case next, ok := <-in:
			if !ok {
				return nil
			}
			segment = next
		case <-ctx.Done():
			return ctx.Err()
		}

		job, err := e.caption(ctx, target, sink, &segment)
		if err != nil {
			return err
		}

		select {
		case jobs <- *job:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// caption translates one segment, ships its caption and returns the take to
// voice. Translation failures stop the lane so the runtime can expose and
// retry them instead of silently publishing an empty rendition.
func (e *Engine) caption(
	ctx context.Context, target core.Lang, sink Sink, segment *timedSegment,
) (*voiceJob, error) {
	text, err := e.translate(ctx, segment.seg, target)
	if err != nil {
		return nil, fmt.Errorf("translate %s to %s: %w", segment.seg.Lang, target, err)
	}

	out := &core.TranslatedSegment{
		Session:    e.stream.Session,
		Track:      e.stream.Track,
		Target:     target,
		Text:       text,
		ScheduleAt: segment.seg.Span[0] + e.stream.Delay,
		Duration:   segment.seg.Span[1] - segment.seg.Span[0],
	}

	sink.Append(out)
	e.metrics.E2ELatency("caption", time.Since(segment.at))
	e.log.Debug("caption",
		"session", e.stream.Session, "target", target,
		"ms", time.Since(segment.at).Milliseconds())

	return &voiceJob{endpointAt: segment.at, seg: out}, nil
}

// translate renders one segment into the target; same-language targets
// short-circuit for free.
func (e *Engine) translate(ctx context.Context, segment Segment, target core.Lang) (string, error) {
	if sameBase(segment.Lang, target) {
		return segment.Text, nil
	}

	return e.providers.Translator.Translate(ctx, segment, target)
}

// track returns the dubbed timeline for a target, or nil when the lane is
// caption-only or the target has no voice track.
func (e *Engine) track(target core.Lang) *pipeline.Track {
	dub := e.output.Dub
	if dub == nil || dub.Synthesizer == nil {
		return nil
	}

	return dub.Tracks[target]
}

// speak synthesizes one take onto its target timeline; chunks spill right from
// the take's scheduled instant, keeping order without overwrite.
func (e *Engine) speak(ctx context.Context, target core.Lang, track *pipeline.Track, job voiceJob) error {
	clauses := make(chan string, 1)
	clauses <- job.seg.Text
	close(clauses)

	audio, err := e.output.Dub.Synthesizer.Speak(ctx, target, e.output.Dub.Voice, clauses)
	if err != nil {
		return fmt.Errorf("synthesize %s: %w", target, err)
	}

	first := true
	for chunk := range audio.Audio() {
		if first {
			e.metrics.E2ELatency("voice", time.Since(job.endpointAt))
			e.log.Debug("voiced",
				"session", job.seg.Session, "target", target, "ms", time.Since(job.endpointAt).Milliseconds())
			first = false
		}

		track.Append(job.seg.ScheduleAt, chunk.Data)
	}
	if streamErr := audio.Err(); streamErr != nil {
		return fmt.Errorf("synthesize %s: %w", target, streamErr)
	}

	return nil
}

// sourceClock is the latest source PTS the pump has reached; the consumer reads
// it to bound each segment on the source timeline.
type sourceClock struct {
	pts atomic.Int64
}

func (c *sourceClock) set(pts time.Duration) { c.pts.Store(int64(pts)) }

func (c *sourceClock) now() time.Duration { return time.Duration(c.pts.Load()) }

// sameBase reports whether two tags share the ISO 639-1 base.
func sameBase(a, b core.Lang) bool {
	baseA, _, _ := strings.Cut(string(a), "-")
	baseB, _, _ := strings.Cut(string(b), "-")

	return baseA != "" && baseA == baseB
}
