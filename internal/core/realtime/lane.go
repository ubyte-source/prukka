package realtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/besteffort"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

const (
	// targetQueue bounds the pending segments a slow target may hold before it
	// stalls the read pump.
	targetQueue = 4
	// maxVoiceTakeSamples mirrors the native helper's 64 MiB WAV ceiling.
	maxVoiceTakeSamples = (64 << 20) / 2
)

// Sink consumes final translated segments for one target language.
type Sink interface {
	Append(seg *core.TranslatedSegment)
}

// Metrics records post-commit pipeline timings; nil disables recording.
type Metrics interface {
	PostCommitLatency(kind string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) PostCommitLatency(string, time.Duration) {}

// Stream identifies the audio a lane processes and how it is timed.
type Stream struct {
	Slug string
	// Source hints the spoken language; core.LangAuto means detect it.
	Source core.Lang
	// Delay is the per-session delay D added to output timing.
	Delay time.Duration
	// FastTurn selects the shorter wait-k policy used by conversational calls.
	FastTurn bool
}

// Providers are the transcription and translation ports the lane drives.
type Providers struct {
	Transcriber Transcriber
	Translator  Translator
}

// Dub is the optional voice stage: streaming synthesis laid onto per-language
// timelines, each target in the voice Voices selects for it.
type Dub struct {
	Synthesizer Synthesizer
	Tracks      map[core.Lang]VoiceSink
	Voices      map[core.Lang]core.Voice
	// Bed receives the original source audio delayed by D; nil if bedless.
	Bed *pipeline.Track
}

// VoiceSink places complete synthesized takes for one target language onto its
// playout; both *pipeline.Track and *pipeline.VoiceQueue satisfy it.
type VoiceSink interface {
	Append(at time.Duration, samples []int16) time.Duration
	ConfigurePlayout(delay time.Duration)
	Finish()
}

// Output routes the lane's results: caption sinks per target language and the
// optional voice stage.
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

// Lane runs one session's streaming pipeline with transcription, translation
// and synthesis overlapping in time.
type Lane struct {
	log       *slog.Logger
	metrics   Metrics
	providers Providers
	output    Output
	stream    Stream
}

// New wires a lane; call Run to start it.
func New(cfg *Config, log *slog.Logger) *Lane {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}

	return &Lane{
		log:       log,
		metrics:   metrics,
		providers: cfg.Providers,
		output:    cfg.Output,
		stream:    cfg.Stream,
	}
}

// timedSegment carries the instant its source clause committed, so the latency
// metric measures only downstream work.
type timedSegment struct {
	at  time.Time
	seg Segment
}

// frameOwner serializes the source's two close paths, cancellation and final
// cleanup, onto a single owner.
type frameOwner func() error

// newFrameOwner adopts closer; a lane run without a source closes nothing.
func newFrameOwner(closer io.Closer) frameOwner {
	if closer == nil {
		return func() error { return nil }
	}

	return sync.OnceValue(closer.Close)
}

// closeForCancel is the cancellation path, which has no caller to report to.
func (o frameOwner) closeForCancel() { besteffort.Ignore(o()) }

// Run drives the pipeline until the source ends or ctx is canceled; it owns
// frames for its duration and closes them.
func (l *Lane) Run(ctx context.Context, frames core.Frames) (runErr error) {
	owned := newFrameOwner(frames)
	defer func() {
		if err := owned(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close source frames: %w", err))
		}
	}()

	if l.stream.Source != core.LangAuto {
		if err := l.validateTranslationTargets(l.stream.Source); err != nil {
			return err
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	group, gctx := errgroup.WithContext(runCtx)
	stopCancelClose := context.AfterFunc(gctx, owned.closeForCancel)
	defer stopCancelClose()

	transcription, err := l.providers.Transcriber.Open(gctx, l.stream.Source)
	if err != nil {
		return fmt.Errorf("open transcription: %w", err)
	}

	l.configurePlayout()

	clock := &sourceClock{}
	workers := l.startTargets(gctx, group)

	group.Go(func() error {
		defer closeTargets(workers)

		return l.consume(gctx, transcription, clock, workers)
	})

	group.Go(func() error {
		return l.pump(gctx, frames, transcription, clock)
	})

	err = group.Wait()
	if err == nil {
		// Gate on the pipeline body alone, before the close error: draining a
		// finished dub must not hinge on how the STT helper reports teardown.
		l.finishDub()
	}
	if closeErr := transcription.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close transcription: %w", closeErr))
	}

	return err
}

// finishDub marks the bed and every voice track complete so the dub drains.
func (l *Lane) finishDub() {
	if l.output.Dub == nil {
		return
	}

	if l.output.Dub.Bed != nil {
		l.output.Dub.Bed.Finish()
	}

	for _, track := range l.output.Dub.Tracks {
		track.Finish()
	}
}

func (l *Lane) configurePlayout() {
	if l.output.Dub == nil {
		return
	}

	if l.output.Dub.Bed != nil {
		l.output.Dub.Bed.ConfigurePlayout(l.stream.Delay)
	}

	for _, track := range l.output.Dub.Tracks {
		track.ConfigurePlayout(l.stream.Delay)
	}
}

// startTargets launches one serial worker per target language.
func (l *Lane) startTargets(ctx context.Context, group *errgroup.Group) map[core.Lang]chan timedSegment {
	workers := make(map[core.Lang]chan timedSegment, len(l.output.Sinks))

	for target, sink := range l.output.Sinks {
		in := make(chan timedSegment, targetQueue)
		workers[target] = in

		group.Go(func() error {
			return l.serveTarget(ctx, target, sink, in)
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
// the bed at PTS+D.
func (l *Lane) pump(
	ctx context.Context, frames core.Frames, transcription Transcription, clock *sourceClock,
) error {
	bed := l.bed()

	for {
		frame, err := frames.Next(ctx)
		if errors.Is(err, io.EOF) {
			return transcription.CloseSend()
		}

		if err != nil {
			return fmt.Errorf("source: %w", err)
		}

		if bed != nil {
			bed.Append(frame.PTS+l.stream.Delay, frame.Data)
		}

		clock.set(frame.PTS)

		if pushErr := transcription.Push(ctx, frame); pushErr != nil {
			return pushErr
		}
	}
}

func (l *Lane) bed() *pipeline.Track {
	if l.output.Dub == nil {
		return nil
	}

	return l.output.Dub.Bed
}

// consume runs the wait-k commit policy over transcript updates and fans the
// committed clauses out to every target worker in order.
func (l *Lane) consume(
	ctx context.Context, transcription Transcription, clock *sourceClock, workers map[core.Lang]chan timedSegment,
) error {
	commit := newCommitter(l.stream.FastTurn)
	cursor := transcriptCursor{lang: l.stream.Source}

	events := transcription.Events()
	for {
		update, ok, err := receiveTranscript(ctx, events)
		if err != nil {
			return err
		}
		if !ok {
			break
		}

		now, err := cursor.advance(update, clock.now())
		if err != nil {
			return err
		}
		if err := l.emitValidated(
			ctx, workers, commit.commit(update), cursor.lang, &cursor.segStart, now,
		); err != nil {
			return err
		}
		// A final closes the source interval even with no text; otherwise the
		// next turn spans the intervening silence and can schedule in the past.
		if update.Final {
			cursor.segStart = now
		}
	}
	if terminalErr := transcription.Err(); terminalErr != nil {
		return fmt.Errorf("transcription: %w", terminalErr)
	}

	return l.emitValidated(
		ctx, workers, commit.flushHold(), cursor.lang, &cursor.segStart, clock.now(),
	)
}

// transcriptCursor tracks the language and source interval across updates.
type transcriptCursor struct {
	lang          core.Lang
	segStart      time.Duration
	lastSourceEnd time.Duration
	hasSourceEnd  bool
}

func receiveTranscript(ctx context.Context, events <-chan Transcript) (Transcript, bool, error) {
	select {
	case update, ok := <-events:
		return update, ok, nil
	case <-ctx.Done():
		return Transcript{}, false, ctx.Err()
	}
}

func (c *transcriptCursor) advance(update Transcript, fallback time.Duration) (time.Duration, error) {
	if update.Lang != "" {
		c.lang = update.Lang
	}
	if !update.HasSourceEnd {
		return fallback, nil
	}
	if update.SourceEnd < 0 || c.hasSourceEnd && update.SourceEnd < c.lastSourceEnd {
		return 0, fmt.Errorf(
			"transcription source time moved backward from %s to %s",
			c.lastSourceEnd, update.SourceEnd,
		)
	}

	c.lastSourceEnd, c.hasSourceEnd = update.SourceEnd, true

	return update.SourceEnd, nil
}

func (l *Lane) emitValidated(
	ctx context.Context, workers map[core.Lang]chan timedSegment,
	clauses []string, lang core.Lang, segStart *time.Duration, now time.Duration,
) error {
	if len(clauses) != 0 {
		if err := l.validateTranslationTargets(lang); err != nil {
			return err
		}
	}

	return l.emit(ctx, workers, clauses, lang, segStart, now)
}

// emit spans committed clauses across the source interval since the last
// commit, splitting it by clause length so each caption gets a duration.
func (l *Lane) emit(
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

		l.log.Debug("clause committed", "session", l.stream.Slug, "lang", lang)

		if !fanout(ctx, workers, &segment) {
			return ctx.Err()
		}
	}

	return nil
}

func (l *Lane) validateTranslationTargets(source core.Lang) error {
	for _, target := range slices.Sorted(maps.Keys(l.output.Sinks)) {
		if source != core.LangAuto && core.SameLang(source, target) {
			continue
		}
		if source == core.LangAuto {
			return fmt.Errorf("translation source language was not detected for target %s", target)
		}
		if !l.providers.Translator.Supports(source, target) {
			return fmt.Errorf("translation model unavailable for %s to %s", source, target)
		}
	}

	return nil
}

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

// serveTarget processes one language's segments, running translation and
// captioning ahead of synthesis so a slow take never stalls the next caption.
func (l *Lane) serveTarget(ctx context.Context, target core.Lang, sink Sink, in <-chan timedSegment) error {
	track := l.track(target)
	if track == nil {
		return l.captionOnly(ctx, target, sink, in)
	}

	targetCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan voiceJob, targetQueue)
	stageErr := make(chan error, 1)
	go func() { stageErr <- l.translateStage(targetCtx, target, sink, in, jobs) }()

	var voiceErr error
	for job := range jobs {
		if err := l.speak(targetCtx, target, track, job); err != nil {
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

func (l *Lane) captionOnly(ctx context.Context, target core.Lang, sink Sink, in <-chan timedSegment) error {
	for segment := range in {
		if _, err := l.caption(ctx, target, sink, &segment); err != nil {
			return err
		}
	}

	return nil
}

// translateStage translates and captions each segment in order and enqueues
// its take for synthesis, closing jobs when the source ends.
func (l *Lane) translateStage(
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

		job, err := l.caption(ctx, target, sink, &segment)
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

// caption translates one segment, ships its caption and returns its take.
func (l *Lane) caption(
	ctx context.Context, target core.Lang, sink Sink, segment *timedSegment,
) (*voiceJob, error) {
	text, err := l.translate(ctx, segment.seg, target)
	if err != nil {
		return nil, fmt.Errorf("translate %s to %s: %w", segment.seg.Lang, target, err)
	}

	out := &core.TranslatedSegment{
		Slug:       l.stream.Slug,
		Text:       text,
		ScheduleAt: segment.seg.Span[0] + l.stream.Delay,
		Duration:   segment.seg.Span[1] - segment.seg.Span[0],
	}

	sink.Append(out)
	l.metrics.PostCommitLatency("caption", time.Since(segment.at))
	l.log.Debug("caption",
		"session", l.stream.Slug, "target", target,
		"ms", time.Since(segment.at).Milliseconds())

	return &voiceJob{endpointAt: segment.at, seg: out}, nil
}

// translate renders one segment into the target; same-language targets
// short-circuit.
func (l *Lane) translate(ctx context.Context, segment Segment, target core.Lang) (string, error) {
	if segment.Lang != core.LangAuto && core.SameLang(segment.Lang, target) {
		return segment.Text, nil
	}

	return l.providers.Translator.Translate(ctx, segment, target)
}

func (l *Lane) track(target core.Lang) VoiceSink {
	dub := l.output.Dub
	if dub == nil || dub.Synthesizer == nil {
		return nil
	}

	return dub.Tracks[target]
}

// sourceClock is the latest source PTS the pump has reached.
type sourceClock struct {
	pts atomic.Int64
}

func (c *sourceClock) set(pts time.Duration) { c.pts.Store(int64(pts)) }

func (c *sourceClock) now() time.Duration { return time.Duration(c.pts.Load()) }
