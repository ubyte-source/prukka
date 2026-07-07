package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ubyte-source/prukka/internal/core"
)

// utteranceQueue decouples the frame pump from provider latency: framing
// never stalls because a transcription is in flight.
const utteranceQueue = 4

// contextWindow is how many previous source lines each translation carries
// for coherent incremental output.
const contextWindow = 2

// Sink consumes final translated segments for one target language. It is a
// consumer-side port; the WebVTT writer satisfies it directly.
type Sink interface {
	Append(seg *core.TranslatedSegment)
}

// Budget gates the paid stages by session spend — dubbing degrades first,
// captions last; nil means no limit.
type Budget interface {
	AllowSTT(session string) bool
	AllowMT(session string) bool
	AllowTTS(session string) bool
}

// Metrics records per-stage and end-to-end timings; nil disables
// recording.
type Metrics interface {
	StageLatency(stage string, d time.Duration)
	E2ELatency(kind string, d time.Duration)
}

// noopMetrics is the default when the config supplies none.
type noopMetrics struct{}

func (noopMetrics) StageLatency(string, time.Duration) {}
func (noopMetrics) E2ELatency(string, time.Duration)   {}

// Dispatcher runs translation/voice jobs on the shared bounded pool; nil
// runs each target in its own goroutine.
type Dispatcher interface {
	Submit(ctx context.Context, fn func()) error
}

// Stream identifies the audio a lane processes and how it is timed.
type Stream struct {
	Session string
	Track   string
	// Source hints the spoken language; LangAuto lets STT detect it.
	Source core.Lang
	// Delay is the per-session delay D added to output timing.
	Delay time.Duration
}

// Providers are the AI and detection ports a lane drives.
type Providers struct {
	STT core.STT
	MT  core.MT
	VAD core.VAD
}

// Output is where a lane's results go: a caption sink per target language,
// and — when Dub is set — the voice stage assembling dubbed audio.
type Output struct {
	// Sinks maps each enabled target language to its caption consumer;
	// per-sink delivery order follows utterance order.
	Sinks map[core.Lang]Sink
	// Dub enables the voice stage; nil keeps the lane captions-only.
	Dub *Dub
}

// Policy carries the cross-cutting runtime knobs; every field is optional.
type Policy struct {
	Budget   Budget
	Metrics  Metrics
	Dispatch Dispatcher
	Glossary map[string]string
}

// CaptionConfig wires one lane in four concern groups; field
// order satisfies the layout linter (scalar-tailed Stream last).
type CaptionConfig struct {
	Providers Providers
	Output    Output
	Policy    Policy
	Stream    Stream
}

// Captions runs one session's pipeline: endpoint, transcribe once,
// translate per target in parallel, deliver to the per-language sinks.
type Captions struct {
	log        *slog.Logger
	speakers   *Speakers
	references *References
	providers  Providers
	output     Output
	policy     Policy
	stream     Stream
}

// NewCaptions wires a lane; call Run to start it.
func NewCaptions(cfg *CaptionConfig, log *slog.Logger) *Captions {
	policy := cfg.Policy
	if policy.Metrics == nil {
		policy.Metrics = noopMetrics{}
	}

	return &Captions{
		log:        log,
		speakers:   NewSpeakers(),
		references: NewReferences(),
		providers:  cfg.Providers,
		output:     cfg.Output,
		policy:     policy,
		stream:     cfg.Stream,
	}
}

// timedUtterance carries the wall-clock instant the utterance was
// endpointed, so end-to-end latency measures from speech end.
type timedUtterance struct {
	at time.Time
	u  core.Utterance
}

// Run drives the pipeline until the source ends or ctx is canceled;
// provider failures degrade single captions, never the lane.
func (c *Captions) Run(ctx context.Context, frames core.Frames) error {
	utterances := make(chan timedUtterance, utteranceQueue)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		// The pump owns the channel: translate ranges until this close.
		defer close(utterances)

		return c.pump(gctx, frames, utterances)
	})

	g.Go(func() error {
		c.translate(gctx, utterances)

		return nil
	})

	return g.Wait()
}

// pump frames the source and endpoints utterances; with dubbing enabled it
// also lays the original audio on the bed track at PTS+D.
func (c *Captions) pump(ctx context.Context, frames core.Frames, out chan<- timedUtterance) error {
	framer := NewFramer()

	emit := func(frame core.PCM) {
		if c.output.Dub != nil && c.output.Dub.Bed != nil {
			c.output.Dub.Bed.Append(frame.PTS+c.stream.Delay, frame.Data)
		}

		c.send(ctx, out, c.providers.VAD.Feed(frame))
	}

	for {
		chunk, err := frames.Next(ctx)
		if errors.Is(err, io.EOF) {
			// The source is done: whatever speech the VAD still holds is
			// the last utterance, not waste.
			c.send(ctx, out, c.providers.VAD.Flush())

			return nil
		}

		if err != nil {
			return fmt.Errorf("source: %w", err)
		}

		if pushErr := framer.Push(chunk, emit); pushErr != nil {
			return pushErr
		}
	}
}

// send stamps utterances with the lane identity and the wall-clock endpoint
// time, then delivers them.
func (c *Captions) send(ctx context.Context, out chan<- timedUtterance, utterances []core.Utterance) {
	now := time.Now()

	for _, u := range utterances {
		u.Session = c.stream.Session
		u.Track = c.stream.Track

		select {
		case out <- timedUtterance{u: u, at: now}:
		case <-ctx.Done():
		}
	}
}

// translate drains utterances: one transcription each, then one translation
// per target. Failures skip the utterance and keep the lane alive.
func (c *Captions) translate(ctx context.Context, utterances <-chan timedUtterance) {
	recent := make([]string, 0, contextWindow)

	for tu := range utterances {
		u := tu.u

		if !c.budgetAllows(budgetSTT) {
			c.log.Warn("budget exhausted; dropping utterance", "session", c.stream.Session)

			continue
		}

		start := time.Now()

		t, err := c.providers.STT.Transcribe(ctx, &u, c.stream.Source)
		if err != nil {
			c.log.Warn("stt failed; dropping utterance", "session", c.stream.Session, "err", err)

			continue
		}

		c.policy.Metrics.StageLatency("stt", time.Since(start))

		if strings.TrimSpace(t.Text) == "" {
			continue
		}

		c.log.Debug("utterance transcribed",
			"session", c.stream.Session,
			"stt_ms", time.Since(start).Milliseconds(),
			"lang", t.Lang,
			"chars", len(t.Text),
		)

		speaker, voice := c.classify(&u)
		c.deliver(ctx, &u, &t, recent, tu.at, speaker, voice)

		recent = appendWindow(recent, t.Text)
	}
}

// classify returns the utterance's stable speaker index and its automatic
// dub voice (cloning reference or register-matched preset).
func (c *Captions) classify(u *core.Utterance) (int, core.Voice) {
	f0 := MedianF0(u.Audio.Data, u.Audio.Rate)

	if c.output.Dub != nil && c.output.Dub.Clone {
		speaker, _ := c.speakers.Classify(f0, nil)

		return speaker, core.Voice{
			ID:  fmt.Sprintf("speaker-%d", speaker),
			Ref: c.references.Capture(speaker, u.Audio),
		}
	}

	var bank []core.Voice
	if c.output.Dub != nil {
		bank = c.output.Dub.AutoVoices
	}

	return c.speakers.Classify(f0, bank)
}

// deliver translates one transcript into every target, returning once all
// land so per-sink ordering follows utterance order.
func (c *Captions) deliver(
	ctx context.Context, u *core.Utterance, t *core.Transcript,
	recent []string, endpointAt time.Time, speaker int, voice core.Voice,
) {
	opts := core.MTOpts{Glossary: c.policy.Glossary, Context: slices.Clone(recent)}

	var wg sync.WaitGroup

	for target, sink := range c.output.Sinks {
		wg.Add(1)

		c.launch(ctx, &wg, func() {
			defer wg.Done()

			c.translateTarget(ctx, u, t, target, sink, opts, endpointAt, speaker, voice)
		})
	}

	wg.Wait()
}

// launch runs job on the dispatcher or a goroutine; a rejected submit
// releases the wait-group ticket here.
func (c *Captions) launch(ctx context.Context, wg *sync.WaitGroup, job func()) {
	if c.policy.Dispatch == nil {
		go job()

		return
	}

	if err := c.policy.Dispatch.Submit(ctx, job); err != nil {
		wg.Done()
	}
}

// translateTarget translates into one target, delivers the caption and
// voices it; a failure drops this caption only.
func (c *Captions) translateTarget(
	ctx context.Context, u *core.Utterance, t *core.Transcript,
	target core.Lang, sink Sink, opts core.MTOpts, endpointAt time.Time, speaker int, voice core.Voice,
) {
	start := time.Now()

	text, err := c.translateOne(ctx, t, target, opts)
	if errors.Is(err, errBudgetPaused) {
		c.log.Debug("budget: translation paused", "session", c.stream.Session, "target", target)

		return
	}

	if err != nil {
		c.log.Warn("mt failed; dropping caption",
			"session", c.stream.Session, "target", target, "err", err)

		return
	}

	c.policy.Metrics.StageLatency("mt", time.Since(start))
	c.policy.Metrics.E2ELatency("caption", time.Since(endpointAt))

	c.log.Debug("caption translated",
		"session", c.stream.Session,
		"target", target,
		"mt_ms", time.Since(start).Milliseconds(),
	)

	seg := &core.TranslatedSegment{
		Session:    c.stream.Session,
		Track:      c.stream.Track,
		Target:     target,
		Text:       text,
		ScheduleAt: u.Audio.PTS + c.stream.Delay,
		Duration:   t.Span[1] - t.Span[0],
		Speaker:    speaker,
	}

	sink.Append(seg)
	c.dub(ctx, seg, endpointAt, voice)
}

// Budget stages the lane gates by spend.
const (
	budgetSTT = "stt"
	budgetMT  = "mt"
	budgetTTS = "tts"
)

// errBudgetPaused signals a stage skipped for budget, not a provider fault.
var errBudgetPaused = errors.New("stage paused for budget")

// budgetAllows reports whether the given paid stage may run for this
// session; a nil Budget in the config means unlimited.
func (c *Captions) budgetAllows(stage string) bool {
	if c.policy.Budget == nil {
		return true
	}

	switch stage {
	case budgetSTT:
		return c.policy.Budget.AllowSTT(c.stream.Session)
	case budgetMT:
		return c.policy.Budget.AllowMT(c.stream.Session)
	case budgetTTS:
		return c.policy.Budget.AllowTTS(c.stream.Session)
	default:
		return true
	}
}

// dub voices one segment onto the target's timeline; a failed take costs
// one line, never the lane — the caption already shipped.
func (c *Captions) dub(ctx context.Context, seg *core.TranslatedSegment, endpointAt time.Time, auto core.Voice) {
	if !c.output.Dub.enabled(seg.Target) {
		return
	}

	if !c.budgetAllows(budgetTTS) {
		c.log.Debug("budget: dubbing paused, caption only", "session", seg.Session, "target", seg.Target)

		return
	}

	voice := c.output.Dub.Voices[seg.Track]
	if voice.ID == "" {
		voice = auto
	}

	start := time.Now()

	take, err := c.output.Dub.TTS.Speak(ctx, seg.Text, seg.Target, voice)
	if err != nil {
		c.log.Warn("tts failed; caption only", "session", seg.Session, "target", seg.Target, "err", err)

		return
	}

	c.policy.Metrics.StageLatency("tts", time.Since(start))

	shapeStart := time.Now()

	tempo := TempoFor(seg.Duration, duration(&take))

	shaped, shapeErr := c.output.Dub.Shaper.Shape(ctx, take, tempo, c.pitchFor(seg.Speaker, &take))
	if shapeErr != nil {
		c.log.Warn("shape failed; caption only", "session", seg.Session, "target", seg.Target, "err", shapeErr)

		return
	}

	c.policy.Metrics.StageLatency("mux", time.Since(shapeStart))

	placed := c.output.Dub.Tracks[seg.Target].Append(seg.ScheduleAt, shaped.Data)
	c.policy.Metrics.E2ELatency("voice", time.Since(endpointAt))

	c.log.Debug("segment dubbed",
		"session", seg.Session,
		"target", seg.Target,
		"voice", voice.ID,
		"tts_ms", time.Since(start).Milliseconds(),
		"placed_at", placed,
	)
}

// pitchFor computes the register-matching shift for one take; 1 unless the
// lane adapts pitch.
func (c *Captions) pitchFor(speaker int, take *core.PCM) float64 {
	if !c.output.Dub.AdaptPitch {
		return 1
	}

	return PitchFor(c.speakers.CenterF0(speaker), MedianF0(take.Data, take.Rate))
}

// translateOne translates a transcript; same-language targets short-circuit
// (free) and over-budget translation pauses.
func (c *Captions) translateOne(
	ctx context.Context, t *core.Transcript, target core.Lang, opts core.MTOpts,
) (string, error) {
	if sameBase(t.Lang, target) {
		return t.Text, nil
	}

	if !c.budgetAllows(budgetMT) {
		return "", errBudgetPaused
	}

	return c.providers.MT.Translate(ctx, *t, target, opts)
}

// sameBase reports whether two tags share the ISO 639-1 base.
func sameBase(a, b core.Lang) bool {
	baseA, _, _ := strings.Cut(string(a), "-")
	baseB, _, _ := strings.Cut(string(b), "-")

	return baseA != "" && baseA == baseB
}

// appendWindow keeps the trailing context lines bounded without growing the
// backing array.
func appendWindow(window []string, line string) []string {
	window = append(window, line)
	if len(window) > contextWindow {
		copy(window, window[len(window)-contextWindow:])
		window = window[:contextWindow]
	}

	return window
}
