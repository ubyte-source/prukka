package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/ingest/file"
	"github.com/ubyte-source/prukka/internal/media/ingest/stream"
	"github.com/ubyte-source/prukka/internal/observability"
	"github.com/ubyte-source/prukka/internal/providers/bounded"
	"github.com/ubyte-source/prukka/internal/providers/native"
)

const valueOff = "off"

// newLaneStarter wires the streaming engine: ingress by URL scheme, one
// transcription/translation/synthesis adapter per configured stage, one sink
// per language. Providers are rebuilt per lane start from the live config.
func newLaneStarter(
	holder *config.Holder, registry *vtt.Registry, audioReg *audio.Registry,
	hlsStore *hls.Store, pool *dispatch.Pool, laneSlots *semaphore.Weighted,
	metrics *observability.Metrics, log *slog.Logger,
) session.LaneStarter {
	return func(ctx context.Context, s *session.Session, running func()) (retErr error) {
		if s.Profile != session.ProfileBroadcast && s.Profile != session.ProfileCall {
			return fmt.Errorf("profile %q does not support media lanes", s.Profile)
		}
		if err := laneSlots.Acquire(ctx, 1); err != nil {
			return err
		}
		defer laneSlots.Release(1)

		cfg := holder.Current()

		providers, buildErr := buildLaneProviders(cfg, pool, log)
		if buildErr != nil {
			return buildErr
		}

		// The warm engines are live from here; until ownership passes to
		// runEngineLane (whose defer closes them), a later setup failure must
		// close them itself instead of leaking them.
		committed := false
		defer func() {
			if !committed {
				retErr = errors.Join(retErr,
					closeLaneProvider(providers.synth), closeLaneProvider(providers.translator))
			}
		}()

		ingress, ingressErr := ingressFor(s.Source.URL, log)
		if ingressErr != nil {
			return ingressErr
		}
		refreshAudioSupervisor(audioReg, log)

		defaultBed, bedErr := core.BedLevel(cfg.Defaults.Bed)
		if bedErr != nil {
			return fmt.Errorf("invalid configured bed level: %w", bedErr)
		}

		committed = true

		return runEngineLane(ctx, &laneDeps{
			session:     s,
			transcriber: providers.transcriber,
			translator:  providers.translator,
			synth:       providers.synth,
			ingress:     ingress,
			out:         laneOutputs{vtt: registry, audio: audioReg, hls: hlsStore},
			metrics:     metrics,
			log:         log,
			voice:       providers.voice,
			defaultBed:  defaultBed,
		}, running)
	}
}

// laneProviders bundles a lane's three built provider stages plus the preset
// voice, handed on to the lane as a unit.
type laneProviders struct {
	transcriber engine.Transcriber
	translator  engine.Translator
	synth       engine.Synthesizer
	voice       core.Voice
}

// buildLaneProviders assembles the transcriber, translator and synthesizer over
// the bundled engine, wrapping the warm engines in the shared pool. On any
// failure it closes whatever it already built, so a half-built lane leaks no
// warm process.
func buildLaneProviders(cfg *config.Config, pool *dispatch.Pool, log *slog.Logger) (laneProviders, error) {
	transcriber, err := newTranscriber(cfg, log)
	if err != nil {
		return laneProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}

	translator, err := newTranslator(cfg)
	if err != nil {
		return laneProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}
	translator = bounded.NewTranslator(pool, translator)

	rawSynth, voice, err := newSynthesizer(cfg, log)
	if err != nil {
		return laneProviders{}, errors.Join(
			fmt.Errorf("dubbing disabled: %w (run `prukka doctor`)", err),
			closeLaneProvider(translator),
		)
	}

	var synth engine.Synthesizer
	if rawSynth != nil {
		synth = bounded.NewSynthesizer(pool, rawSynth)
	}

	return laneProviders{transcriber: transcriber, translator: translator, synth: synth, voice: voice}, nil
}

func refreshAudioSupervisor(registry *audio.Registry, log *slog.Logger) {
	bin, err := ffmpeg.Resolve(config.StateDir())
	if err == nil {
		registry.SetSupervisor(ffmpeg.NewSupervisor(bin, log))
	}
}

// laneOutputs are the egress registries one lane publishes into.
type laneOutputs struct {
	vtt   *vtt.Registry
	audio *audio.Registry
	hls   *hls.Store
}

// laneDeps groups one lane's collaborators by role.
type laneDeps struct {
	session     *session.Session
	transcriber engine.Transcriber
	translator  engine.Translator
	// synth is the voice stage; nil keeps the lane captions-only.
	synth   engine.Synthesizer
	ingress core.Ingress
	out     laneOutputs
	metrics *observability.Metrics
	log     *slog.Logger
	voice   core.Voice
	// defaultBed is the validated fallback bed level from the config snapshot.
	defaultBed float64
}

// errEngineNotInstalled names the configuration needed before a lane can use
// the separately built speech-engine bundle.
var errEngineNotInstalled = errors.New("local speech engine unavailable: configure providers.local.bin")

// installedEngine returns the local provider settings once the bundled engine
// binary is present, or errEngineNotInstalled while it is not.
func installedEngine(providers *config.Providers) (*config.Local, error) {
	if providers.Local.Bin == "" {
		return nil, errEngineNotInstalled
	}

	return &providers.Local, nil
}

// newTranscriber builds the speech-to-text adapter over the bundled local
// engine, spawned over stdio. A missing engine disables lanes rather than
// failing the daemon.
func newTranscriber(cfg *config.Config, log *slog.Logger) (engine.Transcriber, error) {
	localEngine, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	return native.NewSTT(&native.STTConfig{
		Log: log, Bin: localEngine.Bin, Model: localEngine.STT.Model, Rate: pipeline.SampleRate,
		Threads: sttThreads(runtime.GOMAXPROCS(0), cfg.Providers.Dispatch.MaxLanes),
	}), nil
}

// sttThreads derives per-helper parallelism from effective CPU capacity and
// the maximum number of concurrently live helpers.
func sttThreads(cpus, maxLanes int) int {
	return min(4, max(1, cpus/max(1, maxLanes)))
}

// newTranslator builds the translation adapter over the bundled local engine.
func newTranslator(cfg *config.Config) (engine.Translator, error) {
	localEngine, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	pairs := make([]engine.LanguagePair, len(localEngine.MT.Pairs))
	for i, pair := range localEngine.MT.Pairs {
		pairs[i] = engine.LanguagePair{From: pair.From, To: pair.To}
	}

	return native.NewMT(&native.MTConfig{Bin: localEngine.Bin, Pairs: pairs}), nil
}

// newSynthesizer builds the voice stage; voices=off returns a nil synthesizer
// so the lane ships subtitles only. The returned voice is the preset every take
// uses until per-speaker voices land.
func newSynthesizer(cfg *config.Config, log *slog.Logger) (engine.Synthesizer, core.Voice, error) {
	if cfg.Providers.Voices == config.VoicesOff {
		return nil, core.Voice{}, nil
	}

	localEngine, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, core.Voice{}, err
	}

	return native.NewTTS(&native.TTSConfig{Log: log, Bin: localEngine.Bin, Rate: pipeline.SampleRate}),
		core.Voice{ID: localEngine.TTS.Voice, Lang: localEngine.TTS.Language}, nil
}

// ingressFor picks the adapter by URL scheme: WAV files native, everything
// else through the supervised ffmpeg splitter (resolved lazily).
func ingressFor(url string, log *slog.Logger) (core.Ingress, error) {
	scheme, rest, _ := strings.Cut(url, "://")

	switch scheme {
	case "file":
		path, _, _ := strings.Cut(rest, "?")
		if strings.HasSuffix(strings.ToLower(path), ".wav") {
			return file.New(), nil
		}

		fallthrough
	case "rtmp", "srt", "device":
		bin, err := ffmpeg.Resolve(config.StateDir())
		if err != nil {
			return nil, err
		}

		return stream.New(ffmpeg.NewSupervisor(bin, log)), nil
	default:
		return nil, fmt.Errorf("source %q: supported schemes are file://, rtmp://, srt:// and device://", url)
	}
}

// fanoutSink delivers one language's segments to several sinks in order.
type fanoutSink []engine.Sink

// Append implements engine.Sink.
func (f fanoutSink) Append(seg *core.TranslatedSegment) {
	for _, sink := range f {
		sink.Append(seg)
	}
}

type discardSink struct{}

func (discardSink) Append(*core.TranslatedSegment) {}

// captionSinks builds one sink per language: the rolling document, plus the
// HLS subtitle rendition when the tree exists.
func captionSinks(s *session.Session, d *laneDeps, media *hls.Session) map[core.Lang]engine.Sink {
	sinks := make(map[core.Lang]engine.Sink, len(s.Langs))

	for _, target := range s.Langs {
		if s.Flags["subs"] == valueOff {
			sinks[target] = discardSink{}

			continue
		}
		doc := d.out.vtt.Create(s.Slug, target)

		if media != nil {
			sinks[target] = fanoutSink{doc, media.Subtitles(target)}
		} else {
			sinks[target] = doc
		}
	}

	return sinks
}

func createCaptionMedia(s *session.Session, d *laneDeps) *hls.Session {
	subtitleLangs := s.Langs
	if s.Flags["subs"] == valueOff {
		subtitleLangs = nil
	}

	media, err := d.out.hls.CreateWithSubtitles(s.Slug, s.Langs, subtitleLangs)
	if err != nil {
		d.log.Warn("hls tree unavailable; direct endpoints only", "session", s.Slug, "err", err)
	}

	return media
}

// runEngineLane assembles one session's outputs and providers, opens the
// source and runs the streaming engine to completion.
func runEngineLane(ctx context.Context, d *laneDeps, running func()) (retErr error) {
	defer func() {
		retErr = errors.Join(retErr, closeLaneProvider(d.synth), closeLaneProvider(d.translator))
	}()

	s := d.session

	// The HLS tree is best-effort: captions and the direct endpoints work
	// without it (graceful degradation).
	media := createCaptionMedia(s, d)
	sinks := captionSinks(s, d, media)
	dub := buildEngineDub(s, d, media)

	src := s.Source
	if media != nil {
		// An audio-only source has no video to place in the tree: skip the
		// rendition instead of encoding an empty one.
		if !strings.HasPrefix(s.Source.URL, "device://audio/") {
			src.VideoDir = media.VideoDir()
		}
		src.Delay = s.Delay
	}

	frames, openErr := d.ingress.Open(ctx, src)
	if openErr != nil {
		dropLaneOutputs(d, s.Slug)

		return openErr
	}
	frames = &observedFrames{Frames: frames, running: running}

	lane := engine.New(&engine.Config{
		Stream: engine.Stream{
			Session: s.Slug,
			Track:   "main",
			Source:  sourceHint(s),
			Delay:   s.Delay,
		},
		Providers: engine.Providers{Transcriber: d.transcriber, Translator: d.translator},
		Output:    engine.Output{Sinks: sinks, Dub: dub},
		Metrics:   d.metrics,
	}, d.log)

	err := lane.Run(ctx, frames)

	// A clean source end returns nil — a finite file, or a live publisher that
	// stopped the stream — and keeps its completed captions: the delayed dub
	// tail drains before the encoders stop. Only a canceled session or a genuine
	// source failure drops the tree so a retry rebuilds it clean.
	if ctx.Err() != nil || err != nil {
		dropLaneOutputs(d, s.Slug)
	} else {
		err = d.out.audio.WaitPlayout(ctx, s.Slug)
		d.out.audio.Drop(s.Slug)
	}

	return err
}

func closeLaneProvider(provider engine.Closer) error {
	if provider != nil {
		return provider.Close()
	}

	return nil
}

// buildEngineDub wires one mixer per dubbed target and the shared bed; nil
// keeps the lane captions-only (voices off or no dubbed targets).
func buildEngineDub(s *session.Session, d *laneDeps, media *hls.Session) *engine.Dub {
	if d.synth == nil {
		return nil
	}

	targets := supportedDubLanguages(s, d)
	if len(targets) == 0 {
		return nil
	}

	bed := pipeline.NewTrack()
	bedDB := bedLevel(s.Flags["bed"], d.defaultBed)
	tracks := make(map[core.Lang]*pipeline.Track, len(targets))

	for _, target := range targets {
		track := pipeline.NewTrack()
		tracks[target] = track
		d.out.audio.Create(s.Slug, target, pipeline.NewMixer(bed, track, bedDB))
	}

	if media != nil {
		startAudioRenditions(s, d, media, tracks)
	}

	return &engine.Dub{Synthesizer: d.synth, Tracks: tracks, Bed: bed, Voice: d.voice}
}

func supportedDubLanguages(s *session.Session, d *laneDeps) []core.Lang {
	requested := dubbedLanguages(s)
	supported := make([]core.Lang, 0, len(requested))

	for _, target := range requested {
		if d.voice.Supports(target) {
			supported = append(supported, target)

			continue
		}
		if d.log != nil {
			d.log.Warn("dubbing target unsupported by configured voice; caption only",
				"session", s.Slug, "target", target, "voice_language", d.voice.Lang)
		}
	}

	return supported
}

func configuredDubbedLanguages(holder *config.Holder) control.DubbedLanguagesFunc {
	return func(s *session.Session) []core.Lang {
		cfg := holder.Current()
		if cfg.Providers.Voices == config.VoicesOff {
			return nil
		}

		voice := core.Voice{Lang: cfg.Providers.Local.TTS.Language}
		requested := dubbedLanguages(s)
		supported := make([]core.Lang, 0, len(requested))
		for _, target := range requested {
			if voice.Supports(target) {
				supported = append(supported, target)
			}
		}

		return supported
	}
}

func configuredSessionCapability(holder *config.Holder) control.SessionCapabilityFunc {
	return func(s *session.Session) error {
		source := sourceHint(s)
		if source == core.LangAuto {
			return nil
		}

		cfg := holder.Current()
		pairs := make(map[config.TranslationPair]bool, len(cfg.Providers.Local.MT.Pairs))
		for _, pair := range cfg.Providers.Local.MT.Pairs {
			pairs[pair] = true
		}
		from := baseLanguage(source)
		for _, target := range s.Langs {
			to := baseLanguage(target)
			if from == to {
				continue
			}
			if !pairs[config.TranslationPair{From: from, To: to}] {
				return fmt.Errorf("translation model unavailable for %s to %s", source, target)
			}
		}

		return nil
	}
}

func baseLanguage(tag core.Lang) core.Lang {
	base, _, _ := strings.Cut(string(tag), "-")

	return core.Lang(base)
}

func dropLaneOutputs(d *laneDeps, slug string) {
	d.out.vtt.Drop(slug)
	d.out.audio.Drop(slug)
	d.out.hls.Drop(slug)
}

// observedFrames proves a lane is running only after media actually flows.
type observedFrames struct {
	core.Frames

	running func()
	once    sync.Once
}

func (f *observedFrames) Next(ctx context.Context) (core.PCM, error) {
	frame, err := f.Frames.Next(ctx)
	if err == nil {
		f.once.Do(f.running)
	}

	return frame, err
}

// Close preserves lifecycle control exposed by the wrapped ingress. The
// engine uses it to interrupt or release a source even before the first frame
// reaches Next.
func (f *observedFrames) Close() error {
	if closer, ok := f.Frames.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}

// startAudioRenditions launches one rolling HLS encoder per language; a
// failure costs that rendition, never the lane.
func startAudioRenditions(
	s *session.Session, d *laneDeps, media *hls.Session, tracks map[core.Lang]*pipeline.Track,
) {
	for _, target := range s.Langs {
		if tracks[target] == nil {
			continue
		}
		if err := d.out.audio.StartHLS(s.Slug, string(target), media.AudioDir(target), s.Delay); err != nil {
			d.log.Warn("hls audio rendition unavailable", "session", s.Slug, "lang", target, "err", err)
		}
	}
}

func dubbedLanguages(s *session.Session) []core.Lang {
	if s.Flags["dub"] == valueOff {
		return nil
	}
	raw, configured := s.Flags["dub_langs"]
	if !configured {
		return slices.Clone(s.Langs)
	}

	selected := make(map[core.Lang]bool)
	for value := range strings.SplitSeq(raw, ",") {
		if target, parseErr := lang.Parse(strings.TrimSpace(value)); parseErr == nil {
			selected[target] = true
		}
	}

	out := make([]core.Lang, 0, len(selected))
	for _, target := range s.Langs {
		if selected[target] {
			out = append(out, target)
		}
	}

	return out
}

// bedLevel parses a session override and falls back to the already validated
// config snapshot when an internal caller omitted it.
func bedLevel(flag string, fallback float64) float64 {
	level, err := core.BedLevel(flag)
	if err != nil {
		return fallback
	}

	return level
}

// sourceHint reads the optional source-language flag, falling back to
// auto-detection; the flag is validated through the one registry.
func sourceHint(s *session.Session) core.Lang {
	raw, ok := s.Flags["source"]
	if !ok {
		return core.LangAuto
	}

	parsed, err := lang.Parse(raw)
	if err != nil {
		return core.LangAuto
	}

	return parsed
}
