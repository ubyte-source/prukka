package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/meter"
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
	"github.com/ubyte-source/prukka/internal/providers/cartesia"
	"github.com/ubyte-source/prukka/internal/providers/helpers/breaker"
	"github.com/ubyte-source/prukka/internal/providers/helpers/hedge"
	"github.com/ubyte-source/prukka/internal/providers/helpers/retry"
	"github.com/ubyte-source/prukka/internal/providers/local"
	"github.com/ubyte-source/prukka/internal/providers/openrouter"
	"github.com/ubyte-source/prukka/internal/secret"
)

// sttHedgeFloor is the minimum wait before a backup transcription: below
// it, doubling the call costs more than the tail it would cut.
const sttHedgeFloor = time.Second

// newLaneStarter wires the caption path: ingress by URL scheme, the
// configured backend behind the resilience decorators, one sink per
// language. Providers are rebuilt per lane start from the live config.
func newLaneStarter(
	holder *config.Holder, book *meter.Book, registry *vtt.Registry, audioReg *audio.Registry,
	hlsStore *hls.Store, pool *dispatch.Pool,
	metrics *observability.Metrics, fallback *observability.FallbackState, log *slog.Logger,
) session.LaneStarter {
	return func(ctx context.Context, s *session.Session) error {
		if s.Profile != session.ProfileBroadcast && s.Profile != session.ProfileCall {
			return fmt.Errorf("profile %q: media lanes for it arrive in a later release", s.Profile)
		}

		provider, providerErr := newBackend(holder.Current(), meteredCost{book: book, metrics: metrics})
		if providerErr != nil {
			return fmt.Errorf("captions disabled: %w (run `prukka doctor`)", providerErr)
		}

		cloneTTS, _, cloneErr := newCloneTTS(holder.Current())
		if cloneErr != nil {
			return fmt.Errorf("captions disabled: %w (run `prukka doctor`)", cloneErr)
		}

		ingress, ingressErr := ingressFor(s.Source.URL, log)
		if ingressErr != nil {
			return ingressErr
		}

		guard := meter.NewGuard(book, s.BudgetEURPerHour, holder.Current().Budgets.HardStop)

		return runCaptionLane(ctx, &laneDeps{
			session: s,
			backend: provider,
			clone:   cloneTTS,
			adapt:   holder.Current().Providers.Clone == config.ClonePitch,
			ingress: ingress,
			out:     laneOutputs{vtt: registry, audio: audioReg, hls: hlsStore},
			policy:  lanePolicy{guard: guard, dispatch: pool, metrics: metrics, fallback: fallback},
			log:     log,
		})
	}
}

// laneOutputs are the egress registries one lane publishes into.
type laneOutputs struct {
	vtt   *vtt.Registry
	audio *audio.Registry
	hls   *hls.Store
}

// lanePolicy carries the cross-cutting runtime services of one lane: spend
// gating, the shared dispatcher, metrics and breaker observation.
type lanePolicy struct {
	guard    *meter.Guard
	dispatch pipeline.Dispatcher
	metrics  *observability.Metrics
	fallback *observability.FallbackState
}

// laneDeps groups one lane's collaborators by role.
type laneDeps struct {
	session *session.Session
	backend backend
	// clone is the optional timbre-cloning voice, layered over the backend's
	// own TTS. Nil unless a cloning provider is configured.
	clone   core.TTS
	ingress core.Ingress
	out     laneOutputs
	policy  lanePolicy
	log     *slog.Logger
	// adapt turns on in-engine register matching: every take is re-pitched
	// onto its speaker's measured fundamental (providers.clone: pitch).
	adapt bool
}

// ingressFor picks the adapter by URL scheme: WAV files native, everything
// else through the supervised ffmpeg splitter (resolved lazily).
func ingressFor(url string, log *slog.Logger) (core.Ingress, error) {
	scheme, rest, _ := strings.Cut(url, "://")

	switch scheme {
	case "file":
		if strings.HasSuffix(strings.ToLower(rest), ".wav") {
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
type fanoutSink []pipeline.Sink

// Append implements pipeline.Sink.
func (f fanoutSink) Append(seg *core.TranslatedSegment) {
	for _, sink := range f {
		sink.Append(seg)
	}
}

// captionSinks builds one sink per language: the rolling document, plus the
// HLS subtitle rendition when the tree exists.
func captionSinks(s *session.Session, d *laneDeps, media *hls.Session) map[core.Lang]pipeline.Sink {
	sinks := make(map[core.Lang]pipeline.Sink, len(s.Langs))

	for _, target := range s.Langs {
		doc := d.out.vtt.Create(s.Slug, target)

		if media != nil {
			sinks[target] = fanoutSink{doc, media.Subtitles(target)}
		} else {
			sinks[target] = doc
		}
	}

	return sinks
}

// runCaptionLane assembles one session's outputs and providers, opens the
// source and runs the lane to completion.
func runCaptionLane(ctx context.Context, d *laneDeps) error {
	s := d.session

	// The HLS tree is best-effort: captions and the direct endpoints work
	// without it (graceful degradation).
	media, mediaErr := d.out.hls.Create(s.Slug, s.Langs)
	if mediaErr != nil {
		d.log.Warn("hls tree unavailable; direct endpoints only", "session", s.Slug, "err", mediaErr)
	}

	sinks := captionSinks(s, d, media)

	src := s.Source
	if media != nil {
		src.VideoDir = media.VideoDir()
		src.Delay = s.Delay
	}

	frames, openErr := d.ingress.Open(ctx, src)
	if openErr != nil {
		return openErr
	}

	sttPort, mtPort, ttsPort := d.backend.ForSession(s.Slug)
	observe := d.policy.fallback.Observe

	// retry smooths blips, hedge races the p95 tail, breaker degrades to
	// pass-through on sustained failure.
	stt := breaker.STT(
		hedge.NewSTT(retry.STT(sttPort, retry.Default()), sttHedgeFloor),
		breaker.Default(), observe)
	mt := breaker.MT(retry.MT(mtPort, retry.Default()), breaker.Default(), observe)

	dub := voiceStage(d, ttsPort, observe, media)

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream: pipeline.Stream{
			Session: s.Slug,
			Track:   "main",
			Source:  sourceHint(s),
			Delay:   s.Delay,
		},
		Providers: pipeline.Providers{
			STT: stt,
			MT:  mt,
			VAD: pipeline.NewEnergyVAD(vadFor(s.Profile)),
		},
		Output: pipeline.Output{
			Sinks: sinks,
			Dub:   dub,
		},
		Policy: pipeline.Policy{
			Budget:   d.policy.guard,
			Metrics:  d.policy.metrics,
			Dispatch: d.policy.dispatch,
		},
	}, d.log)

	err := lane.Run(ctx, frames)

	// Canceled = session removed: outputs go too. A finished source keeps
	// its outputs downloadable.
	if ctx.Err() != nil {
		d.out.vtt.Drop(s.Slug)
		d.out.audio.Drop(s.Slug)
		d.out.hls.Drop(s.Slug)
	}

	return err
}

// voiceStage builds the optional dubbing stage and starts its HLS audio
// renditions when a tree exists.
func voiceStage(d *laneDeps, tts core.TTS, observe breaker.Observer, media *hls.Session) *pipeline.Dub {
	voices := voicing{preset: tts, clone: d.clone, bank: d.backend.Bank(), adapt: d.adapt}

	dub := buildDub(d.session, voices, d.out.audio, observe, d.log)
	if dub != nil && media != nil {
		startAudioRenditions(d.session, d, media)
	}

	return dub
}

// vadFor picks the endpointing tuning by profile: calls favor
// turn-taking latency, broadcasts caption stability.
func vadFor(profile session.Profile) pipeline.VADConfig {
	if profile == session.ProfileCall {
		return pipeline.CallVAD()
	}

	return pipeline.BroadcastVAD()
}

// startAudioRenditions launches one rolling HLS encoder per language; a
// failure costs that rendition, never the lane.
func startAudioRenditions(s *session.Session, d *laneDeps, media *hls.Session) {
	for _, target := range s.Langs {
		if err := d.out.audio.StartHLS(s.Slug, string(target), media.AudioDir(target), s.Delay); err != nil {
			d.log.Warn("hls audio rendition unavailable", "session", s.Slug, "lang", target, "err", err)
		}
	}
}

// voicing groups one lane's voice sources: preset port, optional cloning
// port, preset bank, and the register-matching switch.
type voicing struct {
	preset core.TTS
	clone  core.TTS
	bank   []core.Voice
	adapt  bool
}

// buildDub wires the voice stage unless dub=off or ffmpeg is missing: one
// mixer per target; a cloning port takes over the voice when configured.
func buildDub(
	s *session.Session, v voicing,
	audioReg *audio.Registry, observe breaker.Observer, log *slog.Logger,
) *pipeline.Dub {
	if s.Flags["dub"] == "off" {
		return nil
	}

	bin, err := ffmpeg.Resolve(config.StateDir())
	if err != nil {
		log.Info("dubbing disabled: no ffmpeg (run `prukka setup`)", "session", s.Slug)

		return nil
	}

	bed := pipeline.NewTrack()
	bedDB := bedLevel(s.Flags["bed"])
	tracks := make(map[core.Lang]*pipeline.Track, len(s.Langs))

	for _, target := range s.Langs {
		track := pipeline.NewTrack()
		tracks[target] = track
		audioReg.Create(s.Slug, target, pipeline.NewMixer(bed, track, bedDB))
	}

	// Auto per-speaker voices are the default; an explicit voice map or
	// voices=manual keeps manual control.
	auto := len(s.VoiceMap) == 0 && s.Flags["voices"] != "manual"

	voice := v.preset
	if v.clone != nil {
		voice = v.clone
	}

	dub := &pipeline.Dub{
		TTS:        breaker.TTS(retry.TTS(voice, retry.Default()), breaker.Default(), observe),
		Shaper:     ffmpeg.NewShaper(ffmpeg.NewSupervisor(bin, log)),
		Voices:     s.VoiceMap,
		Tracks:     tracks,
		Bed:        bed,
		AdaptPitch: v.adapt,
	}

	switch {
	case auto && v.clone != nil:
		dub.Clone = true
	case auto:
		dub.AutoVoices = v.bank
	}

	return dub
}

// bedLevel parses the bed flag ("-15dB") into dB, defaulting to −15 on
// anything unparsable.
func bedLevel(flag string) float64 {
	raw := strings.TrimSuffix(strings.TrimSpace(flag), "dB")

	level, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return -15
	}

	return level
}

// meteredCost fans provider usage to the rate book and the Prometheus
// cost counter.
type meteredCost struct {
	book    *meter.Book
	metrics *observability.Metrics
}

// Add implements core.Meter.
func (m meteredCost) Add(slug, kind string, units, eur float64) {
	m.book.Add(slug, kind, units, eur)
	m.metrics.AddCost(slug, kind, eur)
}

// backend is one AI inference source: per-session STT/MT/TTS ports plus a
// preset voice bank. Hosted and local are equals; cloning layers on top.
type backend interface {
	ForSession(slug string) (core.STT, core.MT, core.TTS)
	Bank() []core.Voice
}

// openRouterBackend meters each session's usage and matches
// register-appropriate preset voices.
type openRouterBackend struct{ client *openrouter.Client }

func (b openRouterBackend) ForSession(slug string) (core.STT, core.MT, core.TTS) {
	c := b.client.ForSession(slug)

	return c, c, c
}

func (openRouterBackend) Bank() []core.Voice { return openrouter.VoiceBank() }

// localBackend runs every stage on the operator's own OpenAI-compatible
// servers; unmetered.
type localBackend struct{ client *local.Client }

func (b localBackend) ForSession(string) (core.STT, core.MT, core.TTS) {
	return b.client, b.client, b.client
}

func (localBackend) Bank() []core.Voice { return local.VoiceBank() }

// newBackend builds the configured backend; a missing OpenRouter key
// disables lanes without failing the daemon.
func newBackend(cfg *config.Config, book core.Meter) (backend, error) {
	if cfg.Providers.Backend == config.BackendLocal {
		return localBackend{client: local.New(localConfig(&cfg.Providers.Local))}, nil
	}

	or := &cfg.Providers.OpenRouter

	key, err := secret.Resolve(or.Key)
	if err != nil {
		return nil, fmt.Errorf("resolve provider key: %w", err)
	}

	if key == "" {
		return nil, errors.New("no OpenRouter key configured")
	}

	return openRouterBackend{client: openrouter.New(&openrouter.Config{
		Endpoint: openrouter.Endpoint{BaseURL: or.BaseURL, Key: key, Timeout: or.Timeout.Std()},
		Models: openrouter.Models{
			STT:         or.STT.Model,
			MT:          or.MT.Model,
			TTS:         or.TTS.Model,
			Temperature: or.MT.Temperature,
		},
		EURPerUSD: or.EURPerUSD,
	}, book)}, nil
}

// localConfig resolves the OpenAI-compatible backend config, defaulting each
// stage's base URL to the shared one when it has no override.
func localConfig(l *config.Local) *local.Config {
	stageURL := func(override string) string {
		if override != "" {
			return override
		}

		return l.BaseURL
	}

	return &local.Config{
		Endpoint: local.Endpoint{
			STT:     stageURL(l.STT.BaseURL),
			MT:      stageURL(l.MT.BaseURL),
			TTS:     stageURL(l.TTS.BaseURL),
			Timeout: l.Timeout.Std(),
		},
		Models: local.Models{
			STT:         l.STT.Model,
			MT:          l.MT.Model,
			TTS:         l.TTS.Model,
			Voice:       l.TTS.Voice,
			Format:      l.TTS.Format,
			Temperature: l.MT.Temperature,
			Rate:        l.TTS.Rate,
		},
	}
}

// newCloneTTS builds the cloning voice when configured; a missing Cartesia
// key disables the lane rather than silently dubbing in a preset.
func newCloneTTS(cfg *config.Config) (tts core.TTS, enabled bool, err error) {
	if cfg.Providers.Clone != config.CloneCartesia {
		return nil, false, nil
	}

	c := &cfg.Providers.Cartesia

	key, err := secret.Resolve(c.Key)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Cartesia key: %w", err)
	}

	if key == "" {
		return nil, false, errors.New("no Cartesia key configured for timbre cloning")
	}

	return cartesia.New(&cartesia.Config{
		BaseURL: c.BaseURL,
		Key:     key,
		Model:   c.Model,
		Timeout: c.Timeout.Std(),
		Rate:    c.Rate,
	}), true, nil
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
