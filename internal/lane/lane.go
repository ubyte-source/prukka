// Package lane assembles and runs one session's media lane: it resolves the
// ingress for the source URL, builds and warms the transcription, translation
// and synthesis providers from the live config, wires the caption and audio
// egress, and drives the streaming lane to completion.
package lane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/observability"
)

// The call profile's latency budget.
const (
	callMediaQuantum = 20 * time.Millisecond
	// callVoiceLead caps the unplayed synthesized backlog of a live call: past
	// it the queue sheds its stalest audio rather than speak it late.
	callVoiceLead = 3 * time.Second
)

// providerWarmTimeout bounds MT and TTS model initialization before capture
// starts.
const providerWarmTimeout = 30 * time.Second

// StarterDeps carries what every lane start reaches for.
type StarterDeps struct {
	Holder  *config.Holder
	Outputs Outputs
	Pool    *dispatch.Pool
	Slots   *semaphore.Weighted
	Metrics *observability.Metrics
	Log     *slog.Logger
}

// NewStarter wires the streaming lane: ingress by URL scheme, one adapter per
// stage, one sink per language, rebuilt per lane start from the live config.
func NewStarter(deps *StarterDeps) session.LaneStarter {
	return func(ctx context.Context, s *session.Session, running func()) (retErr error) {
		if s.Profile != session.ProfileBroadcast && s.Profile != session.ProfileCall {
			return fmt.Errorf("profile %q does not support media lanes", s.Profile)
		}
		if err := deps.Slots.Acquire(ctx, 1); err != nil {
			return err
		}
		defer deps.Slots.Release(1)

		cfg := deps.Holder.Current()
		log := deps.Log
		startup := newStartupObserver(log, s)

		providers, buildErr := buildProviders(ctx, cfg, deps.Pool, s, deps.Metrics, log, startup)
		if buildErr != nil {
			return buildErr
		}

		// The warm engines are live from here; until run takes ownership, a
		// later setup failure must close them itself.
		committed := false
		defer func() {
			if !committed {
				retErr = errors.Join(retErr,
					closeProvider(providers.synth), closeProvider(providers.translator))
			}
		}()

		ingress, ingressErr := ingressFor(s.Source.URL, s.Profile, log)
		if ingressErr != nil {
			return ingressErr
		}
		refreshAudioSupervisor(deps.Outputs.audio, log)

		defaultBed, bedErr := core.BedLevel(string(cfg.Defaults.Bed))
		if bedErr != nil {
			return fmt.Errorf("invalid configured bed level: %w", bedErr)
		}

		committed = true

		return run(ctx, &runDeps{
			session:     s,
			transcriber: providers.transcriber,
			translator:  providers.translator,
			synth:       providers.synth,
			ingress:     ingress,
			out:         deps.Outputs,
			metrics:     deps.Metrics,
			log:         log,
			startup:     startup,
			voices:      providers.voices,
			defaultBed:  defaultBed,
		}, running)
	}
}

// runDeps groups one lane's collaborators by role.
type runDeps struct {
	session     *session.Session
	transcriber realtime.Transcriber
	translator  realtime.Translator
	// synth is the voice stage; nil keeps the lane captions-only.
	synth   realtime.Synthesizer
	ingress core.Ingress
	out     Outputs
	metrics *observability.Metrics
	log     *slog.Logger
	startup *startupObserver
	voices  []core.Voice
	// defaultBed is the validated fallback bed level from the config snapshot.
	defaultBed float64
}

// run assembles one session's outputs and providers, opens the
// source and runs the streaming lane to completion.
func run(ctx context.Context, d *runDeps, running func()) (retErr error) {
	defer func() {
		retErr = errors.Join(retErr, closeProvider(d.synth), closeProvider(d.translator))
	}()

	s := d.session

	// The HLS tree is best-effort: captions and direct endpoints work without it.
	media := createCaptionMedia(s, d)
	sinks := captionSinks(s, d, media)
	dub := buildDub(s, d, media)

	src := s.Source
	if media != nil {
		// An audio-only source has no video to place in the tree.
		if !deviceurl.IsKind(s.Source.URL, deviceurl.Audio) {
			src.VideoDir = media.VideoDir()
		}
		src.Delay = s.Delay
	}

	// Opening a live device starts capture immediately, so keep it lazy: Run
	// waits for the transcriber's readiness handshake before the first frame, or
	// model load accumulates a stale backlog in FFmpeg's stdout pipe.
	mediaStarted := d.startup.begin("waiting_for_media")
	frames := &observedFrames{Frames: newLazyFrames(d.ingress, src),
		running: func() {
			d.startup.complete("media_ready", mediaStarted)
			running()
		},
	}
	transcriber := startupObservedTranscriber{Transcriber: d.transcriber, startup: d.startup}

	// A nil *observability.Metrics boxed bare is a non-nil realtime.Metrics,
	// which would defeat realtime.New's noop fallback.
	var metrics realtime.Metrics
	if d.metrics != nil {
		metrics = d.metrics
	}

	lane := realtime.New(&realtime.Config{
		Stream: realtime.Stream{
			Slug:     s.Slug,
			Source:   s.SourceLang,
			Delay:    s.Delay,
			FastTurn: s.Profile == session.ProfileCall,
		},
		Providers: realtime.Providers{Transcriber: transcriber, Translator: d.translator},
		Output:    realtime.Output{Sinks: sinks, Dub: dub},
		Metrics:   metrics,
	}, d.log)

	err := lane.Run(ctx, frames)

	// A clean source end keeps its completed captions and drains the delayed dub
	// tail; only a cancel or a source failure drops the tree.
	if ctx.Err() != nil || err != nil {
		d.out.Scrub(s.Slug)
	} else {
		err = d.out.audio.WaitPlayout(ctx, s.Slug)
		d.out.audio.Drop(s.Slug)
	}

	return err
}
