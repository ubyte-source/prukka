package lane

import (
	"log/slog"
	"slices"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/paths"
)

// Outputs bundles a session's three output registries so the runtime can tear
// them down together.
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

// Scrub rebuilds a restarting session's output tree but keeps its push routes.
func (o Outputs) Scrub(slug string) {
	o.vtt.Drop(slug)
	o.audio.Reset(slug)
	o.hls.Drop(slug)
}

// refreshAudioSupervisor hands the audio registry the ffmpeg binary resolved
// now, so this lane's encoders use the currently installed one.
func refreshAudioSupervisor(registry *audio.Registry, log *slog.Logger) {
	bin, err := ffmpeg.Resolve(paths.StateDir())
	if err == nil {
		registry.SetSupervisor(ffmpeg.NewSupervisor(bin, log))
	}
}

// fanoutSink delivers one language's segments to several sinks in order.
type fanoutSink []realtime.Sink

// Append implements realtime.Sink.
func (f fanoutSink) Append(seg *core.TranslatedSegment) {
	for _, sink := range f {
		sink.Append(seg)
	}
}

type discardSink struct{}

func (discardSink) Append(*core.TranslatedSegment) {}

// captionSinks builds one sink per language: the rolling document, plus the
// HLS subtitle rendition when the tree exists.
func captionSinks(s *session.Session, d *runDeps, media *hls.Tree) map[core.Lang]realtime.Sink {
	sinks := make(map[core.Lang]realtime.Sink, len(s.Langs))

	for _, target := range s.Langs {
		if s.Subs == session.SubsOff {
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

func createCaptionMedia(s *session.Session, d *runDeps) *hls.Tree {
	// Audio-only calls route directly to local devices; a rolling HLS/AAC tree
	// there burns CPU beside two Whisper lanes. AV calls keep it for pushes.
	if skipCaptionMedia(s) {
		return nil
	}

	subtitleLangs := s.Langs
	if s.Subs == session.SubsOff {
		subtitleLangs = nil
	}

	media, err := d.out.hls.CreateWithSubtitles(s.Slug, s.Langs, subtitleLangs)
	if err != nil {
		d.log.Warn("hls tree unavailable; direct endpoints only", "session", s.Slug, "err", err)
	}

	return media
}

func skipCaptionMedia(s *session.Session) bool {
	return s.Profile == session.ProfileCall && deviceurl.IsKind(s.Source.URL, deviceurl.Audio)
}

// buildDub wires one mixer per dubbed target and the shared bed; nil
// keeps the lane captions-only (voices off or no dubbed targets).
func buildDub(s *session.Session, d *runDeps, media *hls.Tree) *realtime.Dub {
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
	tracks := make(map[core.Lang]realtime.VoiceSink, len(targets))

	if s.Profile == session.ProfileCall {
		// A call injects its live dub straight into the push sink: no bed, no
		// mixer, no delay — a bounded FIFO that drops its stalest backlog.
		for _, target := range targets {
			queue := pipeline.NewVoiceQueue(callVoiceLead)
			tracks[target] = queue
			d.out.audio.Create(s.Slug, target, queue, audio.WithFeedQuantum(callMediaQuantum))
		}

		return &realtime.Dub{Synthesizer: d.synth, Tracks: tracks, Voices: voices}
	}

	// Broadcast mixes each dubbed voice over the shared, delayed bed.
	bed := pipeline.NewTrack()
	bedDB := bedLevelOr(string(s.Bed), d.defaultBed)
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

	return &realtime.Dub{Synthesizer: d.synth, Tracks: tracks, Voices: voices, Bed: bed}
}

func supportedDubLanguages(s *session.Session, d *runDeps) []core.Lang {
	supported := supportedWarmVoices(s, d.voices)
	if d.log == nil {
		return supported
	}

	for _, target := range s.DubbedLangs() {
		if !slices.Contains(supported, target) {
			d.log.Warn("dubbing target has no configured voice; caption only",
				"session", s.Slug, "target", target)
		}
	}

	return supported
}

// startAudioRenditions launches one rolling HLS encoder per language; a
// failure costs that rendition, never the lane.
func startAudioRenditions(
	s *session.Session, d *runDeps, media *hls.Tree, tracks map[core.Lang]*pipeline.Track,
) {
	for _, target := range s.Langs {
		if tracks[target] == nil {
			continue
		}
		if err := d.out.audio.StartHLS(s.Slug, target, media.AudioDir(target), s.Delay); err != nil {
			d.log.Warn("hls audio rendition unavailable", "session", s.Slug, "lang", target, "err", err)
		}
	}
}

// bedLevelOr parses a session override, falling back to the validated config
// snapshot.
func bedLevelOr(flag string, fallback float64) float64 {
	level, err := core.BedLevel(flag)
	if err != nil {
		return fallback
	}

	return level
}
