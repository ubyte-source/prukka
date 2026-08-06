package lane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/nativewire"
	"github.com/ubyte-source/prukka/internal/observability"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/providers/bounded"
	"github.com/ubyte-source/prukka/internal/providers/native"
	"github.com/ubyte-source/prukka/internal/providers/pivot"
	"github.com/ubyte-source/prukka/internal/speech"
)

// builtProviders bundles a lane's three provider stages and its voices.
type builtProviders struct {
	transcriber realtime.Transcriber
	translator  realtime.Translator
	synth       realtime.Synthesizer
	voices      []core.Voice
}

// buildProviders assembles the transcriber, translator and synthesizer over
// the bundled speech engine. Any failure closes whatever it already built, so a
// half-built lane leaks no warm process.
func buildProviders(
	ctx context.Context, cfg *config.Config, pool *dispatch.Pool, s *session.Session,
	metrics *observability.Metrics, log *slog.Logger, startup *startupObserver,
) (builtProviders, error) {
	transcriber, err := newTranscriber(cfg, s.Profile, metrics, log)
	if err != nil {
		return builtProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}

	mt, err := newTranslator(cfg)
	if err != nil {
		return builtProviders{}, fmt.Errorf("captions disabled: %w (run `prukka doctor`)", err)
	}
	// Route any language to any other through the English hub: the bundle ships
	// only en<->X models, so a direct it->de would otherwise report unsupported.
	rawTranslator := pivot.NewTranslator(mt, pivot.English)

	rawSynth, voices, err := newSynthesizer(cfg, log)
	if err != nil {
		return builtProviders{}, errors.Join(
			fmt.Errorf("dubbing disabled: %w (run `prukka doctor`)", err),
			closeProvider(rawTranslator),
		)
	}
	// A nil *native.TTS is the "voices off" state: boxed bare, the typed nil
	// would slip past closeProvider's check and Close on a nil receiver.
	var synthWarmer ttsWarmer
	var synthCloser realtime.Closer
	if rawSynth != nil {
		synthWarmer = rawSynth
		synthCloser = rawSynth
	}
	if err := warmProviders(
		ctx, providerWarmTimeout, pool, s, rawTranslator, synthWarmer, voices, startup,
	); err != nil {
		return builtProviders{}, errors.Join(
			err, closeProvider(synthCloser), closeProvider(rawTranslator),
		)
	}

	translator := bounded.NewTranslator(pool, rawTranslator)

	var synth realtime.Synthesizer
	if rawSynth != nil {
		synth = bounded.NewSynthesizer(pool, rawSynth)
	}

	return builtProviders{transcriber: transcriber, translator: translator, synth: synth, voices: voices}, nil
}

func closeProvider(provider realtime.Closer) error {
	if provider != nil {
		return provider.Close()
	}

	return nil
}

// errSpeechEngineNotInstalled names the remediation for a missing engine.
var errSpeechEngineNotInstalled = errors.New(
	"local speech engine unavailable: run `prukka setup` or configure providers.local.bin")

// installedSpeechEngine returns the local provider settings and the managed
// bundle root. An explicit providers.local.bin wins and self-resolves its
// bundle (empty root); otherwise Bin is this binary and the root is the managed
// bundle the self-executed helpers are pointed at.
func installedSpeechEngine(providers *config.Providers) (local *config.Local, bundleRoot string, err error) {
	if providers.Local.Bin != "" {
		return &providers.Local, "", nil
	}

	root, err := speech.Resolve(paths.StateDir())
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", errSpeechEngineNotInstalled, err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("locate the prukka binary: %w", err)
	}

	resolved := providers.Local
	resolved.Bin = self

	return &resolved, root, nil
}

// newTranscriber builds the speech-to-text adapter over the bundled engine.
func newTranscriber(
	cfg *config.Config, profile session.Profile, metrics *observability.Metrics, log *slog.Logger,
) (realtime.Transcriber, error) {
	localEngine, bundleRoot, err := installedSpeechEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	var observeInference func(string, time.Duration)
	if metrics != nil {
		observeInference = metrics.STTInference
	}
	tuning, fastDecode := sttTuning(profile)

	return native.NewSTT(&native.STTConfig{
		Log:        log,
		Bin:        localEngine.Bin,
		EngineRoot: bundleRoot,
		Model:      sttModel(localEngine.STT, profile),
		Rate:       core.SampleRate,
		Threads:    sttThreads(runtime.GOMAXPROCS(0), cfg.Providers.Dispatch.MaxLanes, profile),
		Tuning:     tuning,
		FastDecode: fastDecode,
		Inference:  observeInference,
	}), nil
}

// sttModel keeps the faster recognition model isolated to conversational lanes;
// an omitted override falls back to the broadcast model.
func sttModel(stt config.LocalSTT, profile session.Profile) string {
	if profile == session.ProfileCall {
		return stt.ModelForCall()
	}

	return stt.Model
}

// sttThreads derives per-helper parallelism from CPU capacity, under the
// eight threads past which whisper.cpp stops scaling. The two directions of
// one call rarely decode at once, so they share one turn budget; broadcast
// lanes divide capacity across every configured lane.
func sttThreads(cpus, maxLanes int, profile session.Profile) int {
	if profile == session.ProfileCall {
		concurrentTurns := max(1, (maxLanes+1)/2)

		return min(8, max(1, cpus/concurrentTurns))
	}

	return min(8, max(1, cpus/max(1, maxLanes)))
}

// sttTuning pins call endpointing explicitly so the helper's defaults can move
// without silently retuning calls; broadcast returns the zero tuning, which
// serializes no flags.
func sttTuning(profile session.Profile) (tuning nativewire.STTTuning, fastDecode bool) {
	if profile != session.ProfileCall {
		return nativewire.STTTuning{}, false
	}

	return nativewire.STTTuning{
		SilenceHang: 300 * time.Millisecond,
		MaxWindow:   5 * time.Second,
		MinSpeech:   250 * time.Millisecond,
	}, true
}

// newTranslator builds the translation adapter over the bundled speech engine.
func newTranslator(cfg *config.Config) (*native.MT, error) {
	localEngine, bundleRoot, err := installedSpeechEngine(&cfg.Providers)
	if err != nil {
		return nil, err
	}

	pairs := make([]realtime.LanguagePair, len(localEngine.MT.Pairs))
	for i, pair := range localEngine.MT.Pairs {
		pairs[i] = realtime.LanguagePair{From: pair.From, To: pair.To}
	}

	return native.NewMT(&native.MTConfig{Bin: localEngine.Bin, EngineRoot: bundleRoot, Pairs: pairs}), nil
}

// newSynthesizer builds the voice stage; voices=off returns a nil synthesizer
// so the lane ships subtitles only.
func newSynthesizer(cfg *config.Config, log *slog.Logger) (*native.TTS, []core.Voice, error) {
	if cfg.Providers.Voices == config.VoicesOff {
		return nil, nil, nil
	}

	localEngine, bundleRoot, err := installedSpeechEngine(&cfg.Providers)
	if err != nil {
		return nil, nil, err
	}

	return native.NewTTS(&native.TTSConfig{
		Log:        log,
		Bin:        localEngine.Bin,
		EngineRoot: bundleRoot,
		Rate:       core.SampleRate,
	}), configuredVoices(cfg), nil
}

// configuredVoices maps the configured voice models onto engine voices,
// preserving order so selection is deterministic.
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

// supportedWarmVoices narrows a session's dubbed targets to the speakable ones.
func supportedWarmVoices(s *session.Session, voices []core.Voice) []core.Lang {
	requested := s.DubbedLangs()
	supported := make([]core.Lang, 0, len(requested))
	for _, target := range requested {
		if _, ok := voiceForTarget(voices, target); ok {
			supported = append(supported, target)
		}
	}

	return supported
}
