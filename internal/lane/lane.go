// Package lane assembles and runs one session's media lane: it resolves the
// ingress for the source URL, builds and warms the transcription, translation
// and synthesis providers from the live config, wires the caption and audio
// egress, and drives the streaming engine until the source ends or the session
// is canceled. The daemon's composition root holds only the wiring; the lane
// domain lives here.
package lane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

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
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/providers/bounded"
	"github.com/ubyte-source/prukka/internal/providers/native"
	"github.com/ubyte-source/prukka/internal/providers/pivot"
	"github.com/ubyte-source/prukka/internal/speech"
)

const (
	callMediaQuantum = 20 * time.Millisecond
	// callVoiceLead caps the unplayed synthesized backlog of a live call: a
	// take arriving behind more than this is dropped rather than spoken stale,
	// bounding mouth-to-ear latency to the pipeline budget plus this lead.
	callVoiceLead   = 3 * time.Second
	callWarmTimeout = 30 * time.Second
)

// laneStartupObserver emits a bounded, source-free lifecycle for startup.
// Durations are integer milliseconds so JSON logs remain easy to aggregate;
// source URLs, model paths, provider errors and voice IDs deliberately never
// enter these records.
type laneStartupObserver struct {
	started time.Time
	log     *slog.Logger
	now     func() time.Time
	profile session.Profile
	session string
}

func newLaneStartupObserver(log *slog.Logger, s *session.Session) *laneStartupObserver {
	return &laneStartupObserver{
		log: log, session: s.Slug, profile: s.Profile, started: time.Now(), now: time.Now,
	}
}

func (o *laneStartupObserver) begin(phase string, attrs ...slog.Attr) time.Time {
	if o == nil {
		return time.Time{}
	}

	now := o.now()
	o.emit(now, phase, attrs...)

	return now
}

func (o *laneStartupObserver) complete(phase string, phaseStarted time.Time) {
	if o == nil {
		return
	}

	now := o.now()
	o.emit(now, phase, slog.Int64("phase_duration_ms", elapsedMilliseconds(now, phaseStarted)))
}

func (o *laneStartupObserver) emit(now time.Time, phase string, attrs ...slog.Attr) {
	if o.log == nil {
		return
	}

	base := make([]slog.Attr, 0, 4+len(attrs))
	base = append(base,
		slog.String("session", o.session),
		slog.String("profile", string(o.profile)),
		slog.String("phase", phase),
		slog.Int64("startup_duration_ms", elapsedMilliseconds(now, o.started)),
	)
	o.log.LogAttrs(context.Background(), slog.LevelInfo, "lane startup", append(base, attrs...)...)
}

func elapsedMilliseconds(now, started time.Time) int64 {
	if elapsed := now.Sub(started).Milliseconds(); elapsed > 0 {
		return elapsed
	}

	return 0
}

// NewStarter wires the streaming engine: ingress by URL scheme, one
// transcription/translation/synthesis adapter per configured stage, one sink
// per language. Providers are rebuilt per lane start from the live config.
func NewStarter(
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
		startup := newLaneStartupObserver(log, s)

		providers, buildErr := buildLaneProviders(ctx, cfg, pool, s, metrics, log, startup)
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

		ingress, ingressErr := ingressFor(s.Source.URL, s.Profile, log)
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
			out:         Outputs{vtt: registry, audio: audioReg, hls: hlsStore},
			metrics:     metrics,
			log:         log,
			startup:     startup,
			voices:      providers.voices,
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
	voices      []core.Voice
}

// buildLaneProviders assembles the transcriber, translator and synthesizer over
// the bundled engine, wrapping the warm engines in the shared pool. On any
// failure it closes whatever it already built, so a half-built lane leaks no
// warm process.
func buildLaneProviders(
	ctx context.Context, cfg *config.Config, pool *dispatch.Pool, s *session.Session,
	metrics *observability.Metrics, log *slog.Logger, startup *laneStartupObserver,
) (laneProviders, error) {
	transcriber, err := newTranscriber(cfg, s.Profile, metrics, log)
	if err != nil {
		return laneProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}

	mt, err := newTranslator(cfg)
	if err != nil {
		return laneProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}
	// Route any language to any other through the English hub: the bundle ships
	// only en<->X models, so a direct it->de would otherwise report unsupported.
	rawTranslator := pivot.NewTranslator(mt, pivot.English)

	rawSynth, voices, err := newSynthesizer(cfg, log)
	if err != nil {
		return laneProviders{}, errors.Join(
			fmt.Errorf("dubbing disabled: %w (run `prukka doctor`)", err),
			closeLaneProvider(rawTranslator),
		)
	}
	var synthWarmer ttsWarmer
	if rawSynth != nil {
		synthWarmer = rawSynth
	}
	if err := warmCallProviders(
		ctx, callWarmTimeout, pool, s, rawTranslator, synthWarmer, voices, startup,
	); err != nil {
		return laneProviders{}, errors.Join(
			err, closeLaneProvider(rawSynth), closeLaneProvider(rawTranslator),
		)
	}

	translator := bounded.NewTranslator(pool, rawTranslator)

	var synth engine.Synthesizer
	if rawSynth != nil {
		synth = bounded.NewSynthesizer(pool, rawSynth)
	}

	return laneProviders{transcriber: transcriber, translator: translator, synth: synth, voices: voices}, nil
}

type mtWarmer interface {
	Warm(ctx context.Context, from, to core.Lang) error
}

type ttsWarmer interface {
	Warm(ctx context.Context, lang core.Lang, voice core.Voice) error
}

// warmCallProviders pays model initialization before capture starts. MT and
// TTS warm concurrently, turning the first live clause into steady-state work
// instead of a sequential multi-second model-load path.
func warmCallProviders(
	ctx context.Context, timeout time.Duration, pool *dispatch.Pool, s *session.Session,
	translator mtWarmer, synth ttsWarmer, voices []core.Voice, startup *laneStartupObserver,
) error {
	if s.Profile != session.ProfileCall {
		return nil
	}

	warmCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mtTasks := mtWarmTasks(s, translator)
	ttsTasks := ttsWarmTasks(s, synth, voices)
	warmStarted := startup.begin(
		"providers_warming", slog.Int("mt_tasks", len(mtTasks)), slog.Int("tts_tasks", len(ttsTasks)),
	)
	tasks := make([]warmTask, 0, len(mtTasks)+len(ttsTasks))
	tasks = append(tasks, mtTasks...)
	tasks = append(tasks, ttsTasks...)
	if err := runWarmTasks(warmCtx, pool, tasks); err != nil {
		startup.complete("providers_failed", warmStarted)

		return fmt.Errorf("warm call providers: %w", err)
	}
	startup.complete("providers_ready", warmStarted)

	return nil
}

type warmTask func(context.Context) error

func mtWarmTasks(s *session.Session, translator mtWarmer) []warmTask {
	tasks := make([]warmTask, 0, len(s.Langs))
	source := sourceHint(s)
	seenPairs := map[string]bool{}
	if source != core.LangAuto {
		for _, target := range s.Langs {
			from, to := baseLanguage(source), baseLanguage(target)
			key := string(from) + ">" + string(to)
			if from == to || seenPairs[key] {
				continue
			}
			seenPairs[key] = true
			tasks = append(tasks, func(ctx context.Context) error {
				return translator.Warm(ctx, from, to)
			})
		}
	}

	return tasks
}

func ttsWarmTasks(s *session.Session, synth ttsWarmer, voices []core.Voice) []warmTask {
	if synth == nil {
		return nil
	}

	tasks := make([]warmTask, 0, len(voices))
	seenVoices := map[string]bool{}
	for _, target := range supportedWarmVoices(s, voices) {
		voice, _ := voiceForTarget(voices, target)
		if seenVoices[voice.ID] {
			continue
		}
		seenVoices[voice.ID] = true
		tasks = append(tasks, func(ctx context.Context) error {
			return synth.Warm(ctx, target, voice)
		})
	}

	return tasks
}

// runWarmTasks sends model initialization through the same bounded pool as
// live MT/TTS work. Submission may backpressure on the configured queue, and
// no more than the configured worker count can load models concurrently.
func runWarmTasks(ctx context.Context, pool *dispatch.Pool, tasks []warmTask) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		pending  sync.WaitGroup
		failOnce sync.Once
		firstErr error
	)
	recordFailure := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for _, task := range tasks {
		pending.Add(1)
		err := pool.Submit(taskCtx, func() {
			defer pending.Done()
			recordFailure(task(taskCtx))
		})
		if err != nil {
			pending.Done()
			recordFailure(err)

			break
		}
	}
	pending.Wait()

	return firstErr
}

func supportedWarmVoices(s *session.Session, voices []core.Voice) []core.Lang {
	requested := dubbedLanguages(s)
	supported := make([]core.Lang, 0, len(requested))
	for _, target := range requested {
		if _, ok := voiceForTarget(voices, target); ok {
			supported = append(supported, target)
		}
	}

	return supported
}

// MicCaptureHelper resolves the managed native audio-device helper. The
// playback wiring (audio.WithPlaybackHelper) and the lane's ffmpeg supervisor
// must agree on one path, so both read it from here.
func MicCaptureHelper() string {
	return ffmpeg.MicCaptureBinary(speech.BundleRoot(paths.StateDir()))
}

func refreshAudioSupervisor(registry *audio.Registry, log *slog.Logger) {
	bin, err := ffmpeg.Resolve(paths.StateDir())
	if err == nil {
		registry.SetSupervisor(ffmpeg.NewSupervisor(bin, log))
	}
}

// Outputs are the egress registries one lane publishes into.
// Outputs bundles a session's three output registries so the runtime can
// tear them down together. Its Drop and Scrub methods are the runtime's
// delete and restart hooks.
type Outputs struct {
	vtt   *vtt.Registry
	audio *audio.Registry
	hls   *hls.Store
}

// NewOutputs binds the three output registries the lanes write into.
func NewOutputs(docs *vtt.Registry, streams *audio.Registry, media *hls.Store) Outputs {
	return Outputs{vtt: docs, audio: streams, hls: media}
}

// Drop forgets a deleted session's outputs entirely.
func (o Outputs) Drop(slug string) {
	o.vtt.Drop(slug)
	o.audio.Drop(slug)
	o.hls.Drop(slug)
}

// Scrub rebuilds a restarting session's output tree but keeps its push
// routes: they are user intents and relaunch on the rebuilt pairs.
func (o Outputs) Scrub(slug string) {
	o.vtt.Drop(slug)
	o.audio.Reset(slug)
	o.hls.Drop(slug)
}

// laneDeps groups one lane's collaborators by role.
type laneDeps struct {
	session     *session.Session
	transcriber engine.Transcriber
	translator  engine.Translator
	// synth is the voice stage; nil keeps the lane captions-only.
	synth   engine.Synthesizer
	ingress core.Ingress
	out     Outputs
	metrics *observability.Metrics
	log     *slog.Logger
	startup *laneStartupObserver
	voices  []core.Voice
	// defaultBed is the validated fallback bed level from the config snapshot.
	defaultBed float64
}

// errEngineNotInstalled names the remediation when neither an operator
// bundle nor the managed install provides the speech engine.
var errEngineNotInstalled = errors.New(
	"local speech engine unavailable: run `prukka setup` or configure providers.local.bin")

// installedEngine returns the local provider settings and the managed bundle
// root once an engine is present. An explicit providers.local.bin wins and
// self-resolves its bundle (empty root); otherwise the daemon self-executes its
// own hidden engine helpers against the managed bundle installed by
// `prukka setup` or the dashboard, so Bin is this binary and root points those
// helpers at the bundle through PRUKKA_ENGINE_ROOT.
func installedEngine(providers *config.Providers) (local *config.Local, engineRoot string, err error) {
	if providers.Local.Bin != "" {
		return &providers.Local, "", nil
	}

	root, err := speech.Resolve(paths.StateDir())
	if err != nil {
		return nil, "", errEngineNotInstalled
	}
	self, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("locate the prukka binary: %w", err)
	}

	resolved := providers.Local
	resolved.Bin = self

	return &resolved, root, nil
}

// newTranscriber builds the speech-to-text adapter over the bundled local
// engine, spawned over stdio. A missing engine disables lanes rather than
// failing the daemon.
func newTranscriber(
	cfg *config.Config, profile session.Profile, metrics *observability.Metrics, log *slog.Logger,
) (engine.Transcriber, error) {
	localEngine, engineRoot, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	var observeInference func(string, time.Duration)
	if metrics != nil {
		observeInference = metrics.STTInference
	}

	return native.NewSTT(&native.STTConfig{
		Log: log, Bin: localEngine.Bin, EngineRoot: engineRoot,
		Model: sttModel(localEngine.STT, profile), Rate: core.SampleRate,
		Threads: sttThreads(runtime.GOMAXPROCS(0), cfg.Providers.Dispatch.MaxLanes, profile),
		Tuning:  sttTuning(profile), Inference: observeInference,
	}), nil
}

// sttModel keeps the faster recognition model isolated to conversational
// lanes. An omitted override deliberately falls back to the broadcast model so
// existing single-model engine bundles continue to work.
func sttModel(stt config.LocalSTT, profile session.Profile) string {
	if profile == session.ProfileCall {
		return stt.ModelForCall()
	}

	return stt.Model
}

// sttThreads derives per-helper parallelism from effective CPU capacity. Call
// lanes favor turn latency: the two directions of one conversation are
// normally not decoding at the same instant, so they share one concurrent-turn
// budget. Additional configured call pairs divide the machine between those
// possible turns. Broadcast lanes divide capacity across every configured lane
// because they can remain continuously active.
func sttThreads(cpus, maxLanes int, profile session.Profile) int {
	if profile == session.ProfileCall {
		concurrentTurns := max(1, (maxLanes+1)/2)

		return min(8, max(1, cpus/concurrentTurns))
	}

	return min(4, max(1, cpus/max(1, maxLanes)))
}

// sttTuning pins calls to sentence-sized windows and a bounded 10.24 s Whisper
// context. The former 2 s cut split words; the former 3.84 s context sat too
// close to longer clauses and could trap the decoder until timeout. A partial
// stride equal to the hard window prevents speculative decodes from delaying
// the endpointed final on CPU-only machines.
func sttTuning(profile session.Profile) native.STTTuning {
	if profile != session.ProfileCall {
		return native.STTTuning{}
	}

	return native.STTTuning{
		SilenceHang: 300 * time.Millisecond, MaxWindow: 5 * time.Second,
		MinSpeech: 250 * time.Millisecond, PartialStride: 5 * time.Second,
		FastDecode: true,
	}
}

// newTranslator builds the translation adapter over the bundled local engine.
func newTranslator(cfg *config.Config) (*native.MT, error) {
	localEngine, engineRoot, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	pairs := make([]engine.LanguagePair, len(localEngine.MT.Pairs))
	for i, pair := range localEngine.MT.Pairs {
		pairs[i] = engine.LanguagePair{From: pair.From, To: pair.To}
	}

	return native.NewMT(&native.MTConfig{Bin: localEngine.Bin, EngineRoot: engineRoot, Pairs: pairs}), nil
}

// newSynthesizer builds the voice stage; voices=off returns a nil synthesizer
// so the lane ships subtitles only. The returned voices are the configured set,
// one per language, that later selection picks from per dubbed target.
func newSynthesizer(cfg *config.Config, log *slog.Logger) (*native.TTS, []core.Voice, error) {
	if cfg.Providers.Voices == config.VoicesOff {
		return nil, nil, nil
	}

	localEngine, engineRoot, err := installedEngine(&cfg.Providers)
	if err != nil {
		return nil, nil, err
	}

	return native.NewTTS(&native.TTSConfig{
		Log: log, Bin: localEngine.Bin, EngineRoot: engineRoot, Rate: core.SampleRate,
	}), configuredVoices(cfg), nil
}

// configuredVoices maps the configured voice models onto engine voices, one per
// language, preserving order so selection is deterministic.
func configuredVoices(cfg *config.Config) []core.Voice {
	models := cfg.Providers.Local.TTS.Voices
	voices := make([]core.Voice, len(models))
	for i := range models {
		voices[i] = core.Voice{ID: models[i].Voice, Lang: models[i].Language}
	}

	return voices
}

// voiceForTarget returns the first configured voice that can synthesize target.
func voiceForTarget(voices []core.Voice, target core.Lang) (core.Voice, bool) {
	for _, v := range voices {
		if v.Supports(target) {
			return v, true
		}
	}

	return core.Voice{}, false
}

// ingressFor picks the adapter by URL scheme: WAV files native, everything
// else through the supervised ffmpeg splitter (resolved lazily).
func ingressFor(url string, profile session.Profile, log *slog.Logger) (core.Ingress, error) {
	scheme, rest, _ := strings.Cut(url, "://")

	switch scheme {
	case "file":
		path, _, _ := strings.Cut(rest, "?")
		if strings.HasSuffix(strings.ToLower(path), ".wav") {
			options := []file.Option(nil)
			if profile == session.ProfileCall {
				options = append(options, file.WithPCMQuantum(callMediaQuantum))
			}

			return file.New(options...), nil
		}

		fallthrough
	case "rtmp", "srt", "device":
		bin, err := ffmpeg.Resolve(paths.StateDir())
		if err != nil {
			return nil, err
		}

		supervisorOptions := []ffmpeg.SupervisorOption(nil)
		if helper := MicCaptureHelper(); helper != "" {
			supervisorOptions = append(supervisorOptions, ffmpeg.WithMicCapture(helper))
		}

		options := []stream.Option(nil)
		if profile == session.ProfileCall {
			options = append(options,
				stream.WithPCMQuantum(callMediaQuantum),
				stream.WithDeviceCaptureBuffer(callMediaQuantum),
			)
		}

		return stream.New(ffmpeg.NewSupervisor(bin, log, supervisorOptions...), options...), nil
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
		if s.Flags["subs"] == session.Off {
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
	// Audio-only calls route directly to local devices. Building a rolling
	// HLS/AAC tree there burns CPU beside two Whisper lanes and adds no value.
	// AV calls retain the tree because it is the source for video/device pushes.
	if skipCaptionMedia(s) {
		return nil
	}

	subtitleLangs := s.Langs
	if s.Flags["subs"] == session.Off {
		subtitleLangs = nil
	}

	media, err := d.out.hls.CreateWithSubtitles(s.Slug, s.Langs, subtitleLangs)
	if err != nil {
		d.log.Warn("hls tree unavailable; direct endpoints only", "session", s.Slug, "err", err)
	}

	return media
}

func skipCaptionMedia(s *session.Session) bool {
	return s.Profile == session.ProfileCall && strings.HasPrefix(s.Source.URL, "device://audio/")
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

	// Opening a live device starts capture immediately. Keep that operation lazy:
	// Engine.Run opens the transcriber and waits for its readiness handshake
	// before its pump requests the first frame, so model load cannot accumulate a
	// stale call backlog in FFmpeg's stdout pipe.
	mediaStarted := d.startup.begin("waiting_for_media")
	frames := &observedFrames{Frames: newLazyFrames(d.ingress, src), running: func() {
		d.startup.complete("media_ready", mediaStarted)
		running()
	}}
	transcriber := startupObservedTranscriber{Transcriber: d.transcriber, startup: d.startup}

	lane := engine.New(&engine.Config{
		Stream: engine.Stream{
			Session:  s.Slug,
			Track:    "main",
			Source:   sourceHint(s),
			Delay:    s.Delay,
			FastTurn: s.Profile == session.ProfileCall,
		},
		Providers: engine.Providers{Transcriber: transcriber, Translator: d.translator},
		Output:    engine.Output{Sinks: sinks, Dub: dub},
		Metrics:   d.metrics,
	}, d.log)

	err := lane.Run(ctx, frames)

	// A clean source end returns nil — a finite file, or a live publisher that
	// stopped the stream — and keeps its completed captions: the delayed dub
	// tail drains before the encoders stop. Only a canceled session or a genuine
	// source failure drops the tree so a retry rebuilds it clean.
	if ctx.Err() != nil || err != nil {
		d.out.Scrub(s.Slug)
	} else {
		err = d.out.audio.WaitPlayout(ctx, s.Slug)
		d.out.audio.Drop(s.Slug)
	}

	return err
}

// startupObservedTranscriber brackets the native STT readiness handshake. An
// error is represented only by its phase: the runtime owns sanitized failure
// reporting, so provider messages and local model paths cannot leak here.
type startupObservedTranscriber struct {
	engine.Transcriber

	startup *laneStartupObserver
}

func (t startupObservedTranscriber) Open(
	ctx context.Context, source core.Lang,
) (engine.Transcription, error) {
	warmStarted := t.startup.begin("transcription_warming")
	transcription, err := t.Transcriber.Open(ctx, source)
	if err != nil {
		t.startup.complete("transcription_failed", warmStarted)

		return nil, err
	}
	t.startup.complete("transcription_ready", warmStarted)

	return transcription, nil
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

	voices := make(map[core.Lang]core.Voice, len(targets))
	for _, target := range targets {
		voices[target], _ = voiceForTarget(d.voices, target)
	}
	tracks := make(map[core.Lang]engine.VoiceSink, len(targets))

	if s.Profile == session.ProfileCall {
		// A call injects its live dub straight into the push sink: no bed to
		// duck, no mixer, no delay — a bounded FIFO that speaks each take as it
		// is synthesized and drops its stalest backlog to stay near real time.
		for _, target := range targets {
			queue := pipeline.NewVoiceQueue(callVoiceLead)
			tracks[target] = queue
			d.out.audio.Create(s.Slug, target, queue, audio.WithFeedQuantum(callMediaQuantum))
		}

		return &engine.Dub{Synthesizer: d.synth, Tracks: tracks, Voices: voices}
	}

	// Broadcast mixes each dubbed voice over the shared, delayed bed.
	bed := pipeline.NewTrack()
	bedDB := bedLevel(s.Flags["bed"], d.defaultBed)
	renditions := make(map[core.Lang]*pipeline.Track, len(targets))

	for _, target := range targets {
		track := pipeline.NewTrack()
		tracks[target] = track
		renditions[target] = track
		d.out.audio.Create(s.Slug, target, pipeline.NewMixer(bed, track, bedDB))
	}

	if media != nil {
		startAudioRenditions(s, d, media, renditions)
	}

	return &engine.Dub{Synthesizer: d.synth, Tracks: tracks, Voices: voices, Bed: bed}
}

func supportedDubLanguages(s *session.Session, d *laneDeps) []core.Lang {
	supported := supportedWarmVoices(s, d.voices)
	if d.log == nil {
		return supported
	}

	for _, target := range dubbedLanguages(s) {
		if !slices.Contains(supported, target) {
			d.log.Warn("dubbing target has no configured voice; caption only",
				"session", s.Slug, "target", target)
		}
	}

	return supported
}

// DubbedLanguages reports, for the control service, which target languages a
// session actually voices under the live config (a target with no voice is
// captioned only). It reads the config snapshot on each call.
func DubbedLanguages(holder *config.Holder) func(*session.Session) []core.Lang {
	return func(s *session.Session) []core.Lang {
		cfg := holder.Current()
		if cfg.Providers.Voices == config.VoicesOff {
			return nil
		}

		return supportedWarmVoices(s, configuredVoices(cfg))
	}
}

// SessionCapability validates a session request against the live config for
// the control service: it rejects targets the installed engine cannot serve
// before a lane is ever started.
func SessionCapability(holder *config.Holder) func(*session.Session) error {
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
		// The runtime translator bridges through the English hub, so admission
		// must judge support the same way — a raw direct-pair lookup would
		// reject exactly the it->en->de sessions the pivot exists to serve.
		direct := func(from, to core.Lang) bool {
			return pairs[config.TranslationPair{From: from, To: to}]
		}
		from := baseLanguage(source)
		for _, target := range s.Langs {
			to := baseLanguage(target)
			if from == to {
				continue
			}
			if !pivot.Supported(direct, pivot.English, from, to) {
				return fmt.Errorf("translation model unavailable for %s to %s", source, target)
			}
		}

		return nil
	}
}

func baseLanguage(tag core.Lang) core.Lang {
	return tag.Base()
}

// lazyFrames defers opening a source until the engine first asks for media.
// Engine.Run establishes STT readiness before that first pull. Close may race
// the initial Open: if an ingress returns a source after cancellation, the
// opener closes that eventual result instead of publishing or leaking it.
type lazyFrames struct {
	ingress core.Ingress
	frames  core.Frames
	source  *core.SourceSpec

	openErr         error
	closeErr        error
	sourceCloseDone chan struct{}
	openOnce        sync.Once
	mu              sync.Mutex
	closed          bool
	sourceClosing   bool
	sourceClosed    bool
}

func newLazyFrames(ingress core.Ingress, source core.SourceSpec) *lazyFrames {
	return &lazyFrames{ingress: ingress, source: &source}
}

func (f *lazyFrames) Next(ctx context.Context) (core.PCM, error) {
	if err := ctx.Err(); err != nil {
		return core.PCM{}, err
	}

	f.openOnce.Do(func() { f.open(ctx) })
	if err := ctx.Err(); err != nil {
		return core.PCM{}, err
	}

	f.mu.Lock()
	frames, openErr, closed := f.frames, f.openErr, f.closed
	f.mu.Unlock()

	if openErr != nil {
		return core.PCM{}, openErr
	}
	if closed {
		return core.PCM{}, io.ErrClosedPipe
	}
	if frames == nil {
		return core.PCM{}, errors.New("lazy ingress returned no frames")
	}

	return frames.Next(ctx)
}

func (f *lazyFrames) open(ctx context.Context) {
	f.mu.Lock()
	if f.closed {
		f.openErr = io.ErrClosedPipe
		f.mu.Unlock()

		return
	}
	f.mu.Unlock()

	frames, openErr := f.ingress.Open(ctx, *f.source)
	if frames == nil && openErr == nil {
		openErr = errors.New("ingress returned no frames")
	}
	ctxErr := ctx.Err()

	f.mu.Lock()
	closed := f.closed
	if openErr == nil && ctxErr == nil && !closed {
		f.frames = frames
		f.mu.Unlock()

		return
	}
	if closed {
		openErr = errors.Join(openErr, io.ErrClosedPipe)
	}
	openErr = errors.Join(openErr, ctxErr)
	f.openErr = openErr
	f.mu.Unlock()

	if frames != nil {
		closeErr := f.closeSource(frames)
		f.mu.Lock()
		f.openErr = errors.Join(f.openErr, closeErr)
		f.mu.Unlock()
	}
}

// Close prevents a future open and interrupts an already-open source. It does
// not wait for an ingress Open that is still in progress; that goroutine owns
// closing any source it eventually receives. Concurrent closes of a published
// source wait for the same underlying Close and return its cached result.
func (f *lazyFrames) Close() error {
	f.mu.Lock()
	f.closed = true
	frames := f.frames
	if frames == nil {
		done := f.sourceCloseDone
		closing := f.sourceClosing
		closeErr := f.closeErr
		f.mu.Unlock()

		if closing {
			<-done
			f.mu.Lock()
			closeErr = f.closeErr
			f.mu.Unlock()
		}

		return closeErr
	}
	f.mu.Unlock()

	return f.closeSource(frames)
}

func (f *lazyFrames) closeSource(frames core.Frames) error {
	f.mu.Lock()
	if f.sourceClosed {
		closeErr := f.closeErr
		f.mu.Unlock()

		return closeErr
	}
	if f.sourceClosing {
		done := f.sourceCloseDone
		f.mu.Unlock()
		<-done
		f.mu.Lock()
		closeErr := f.closeErr
		f.mu.Unlock()

		return closeErr
	}
	f.sourceClosing = true
	f.sourceCloseDone = make(chan struct{})
	done := f.sourceCloseDone
	f.mu.Unlock()

	var closeErr error
	if closer, ok := frames.(io.Closer); ok {
		closeErr = closer.Close()
	}

	f.mu.Lock()
	f.frames = nil
	f.closeErr = errors.Join(f.closeErr, closeErr)
	f.sourceClosing = false
	f.sourceClosed = true
	close(done)
	closeErr = f.closeErr
	f.mu.Unlock()

	return closeErr
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
	if s.Flags["dub"] == session.Off {
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
