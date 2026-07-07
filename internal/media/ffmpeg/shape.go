package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"math"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// tempoDeadband: within ±3% no stretch is applied and no ffmpeg spawns —
// imperceptible on speech, covers the common case.
const tempoDeadband = 0.03

// pitchDeadband: under ±2% a register shift is inaudible and not worth a
// subprocess.
const pitchDeadband = 0.02

// Shaper implements pipeline.Shaper; a take inside both deadbands is
// resampled in-process without a subprocess.
type Shaper struct {
	sup *Supervisor
}

// Compile-time port check.
var _ pipeline.Shaper = Shaper{}

// NewShaper wires a shaper around the supervisor's binary.
func NewShaper(sup *Supervisor) Shaper {
	return Shaper{sup: sup}
}

// Shape implements pipeline.Shaper.
func (s Shaper) Shape(ctx context.Context, audio core.PCM, tempo, pitch float64) (core.PCM, error) {
	if len(audio.Data) == 0 {
		return core.PCM{}, nil
	}

	// Fast path: no meaningful stretch or shift — resample in-process,
	// no ffmpeg.
	if math.Abs(tempo-1.0) <= tempoDeadband && math.Abs(pitch-1.0) <= pitchDeadband {
		return pipeline.Resample(audio, pipeline.SampleRate), nil
	}

	args := argv(quietArgs,
		s16le(audio.Rate, audio.Ch), []string{flagInput, pipeIn},
		[]string{"-filter:a", filter(audio.Rate, tempo, pitch)},
		s16le(pipeline.SampleRate, 1), []string{pipeOut})

	cmd := newCommand(ctx, s.sup.bin, args)

	in, encodeErr := pipeline.EncodeS16LE(audio.Data)
	if encodeErr != nil {
		return core.PCM{}, encodeErr
	}

	cmd.Stdin = bytes.NewReader(in)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return core.PCM{}, fmt.Errorf("shape audio: %w: %s", err, stderr.String())
	}

	samples := make([]int16, len(out)/2)
	if _, decodeErr := pipeline.DecodeS16LE(samples, out); decodeErr != nil {
		return core.PCM{}, decodeErr
	}

	return core.PCM{Data: samples, Rate: pipeline.SampleRate, Ch: 1, PTS: audio.PTS}, nil
}

// filter renders the take's filter chain: asetrate shifts pitch and speed
// together, so atempo divides by pitch — duration stays 1/tempo exactly.
func filter(rate int, tempo, pitch float64) string {
	if math.Abs(pitch-1.0) <= pitchDeadband {
		return fmt.Sprintf("atempo=%.4f", tempo)
	}

	return fmt.Sprintf("asetrate=%d,atempo=%.4f", int(float64(rate)*pitch), tempo/pitch)
}
