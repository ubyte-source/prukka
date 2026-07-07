package pipeline

import (
	"context"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Isochrony bounds: a take may be slowed to 0.85× or sped to 1.20×;
// anything longer spills via the Track.
const (
	minTempo = 0.85
	maxTempo = 1.20
)

// Register bounds: at most ~4 semitones of re-pitch either way — past
// that the formants drift audibly.
const (
	minPitch = 0.80
	maxPitch = 1.25
)

// Shaper adapts a take onto the timeline: resample, time-stretch by tempo,
// re-pitch by pitch (1 = untouched). The ffmpeg adapter implements it.
type Shaper interface {
	Shape(ctx context.Context, audio core.PCM, tempo, pitch float64) (core.PCM, error)
}

// Dub wires the optional dubbing stage of a lane: text-to-speech
// per target, isochrony shaping and timeline assembly.
type Dub struct {
	TTS    core.TTS
	Shaper Shaper
	// Voices maps Track identity to a voice (voice bank); the
	// provider falls back to its default voice for missing entries.
	Voices map[string]core.Voice
	// Tracks holds one timeline per dubbed language.
	Tracks map[core.Lang]*Track
	// Bed receives the original source audio, delayed by D, so the mixer
	// can keep it under the dubbed voice. Optional.
	Bed *Track
	// AutoVoices, when non-empty, turns on automatic per-speaker voices
	// from this bank (ordered deep → bright); an explicit entry wins.
	AutoVoices []core.Voice
	// Clone dubs each speaker in their own captured voice; needs a
	// cloning-capable provider, an explicit Voices entry wins.
	Clone bool
	// AdaptPitch re-pitches each take onto its speaker's fundamental —
	// register matching with any backend, no cloning provider.
	AdaptPitch bool
}

// enabled reports whether this lane dubs the given target.
func (d *Dub) enabled(target core.Lang) bool {
	return d != nil && d.TTS != nil && d.Shaper != nil && d.Tracks[target] != nil
}

// TempoFor computes the atempo factor that fits a take into the source
// utterance, clamped to the readable range.
func TempoFor(source, take time.Duration) float64 {
	if source <= 0 || take <= 0 {
		return 1
	}

	tempo := float64(take) / float64(source)

	switch {
	case tempo < minTempo:
		return minTempo
	case tempo > maxTempo:
		return maxTempo
	default:
		return tempo
	}
}

// PitchFor computes the clamped rate shift that moves a take's fundamental
// onto the speaker's; a missing fundamental leaves the take untouched.
func PitchFor(speaker, take float64) float64 {
	if speaker <= 0 || take <= 0 {
		return 1
	}

	pitch := speaker / take

	switch {
	case pitch < minPitch:
		return minPitch
	case pitch > maxPitch:
		return maxPitch
	default:
		return pitch
	}
}

// duration reports a PCM's play time.
func duration(p *core.PCM) time.Duration {
	if p.Rate <= 0 || p.Ch <= 0 {
		return 0
	}

	return time.Duration(len(p.Data)/p.Ch) * time.Second / time.Duration(p.Rate)
}
