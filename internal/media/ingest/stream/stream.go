// Package stream implements the rtmp:// and srt:// ingresses over the
// supervised ffmpeg demuxer.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// chunkSamples sizes one Next delivery: 100 ms of reference audio.
const chunkSamples = pipeline.SampleRate / 10

// Ingress opens network sources through ffmpeg.
type Ingress struct {
	sup *ffmpeg.Supervisor
}

// Compile-time port check.
var _ core.Ingress = Ingress{}

// New wires the ingress around a supervisor.
func New(sup *ffmpeg.Supervisor) Ingress {
	return Ingress{sup: sup}
}

// Open implements core.Ingress. A non-empty VideoDir turns on the source's
// passthrough HLS video rendition alongside the PCM tap.
func (i Ingress) Open(ctx context.Context, src core.SourceSpec) (core.Frames, error) {
	pcm, err := i.sup.StartPCM(ctx, src.URL, src.VideoDir, src.Delay)
	if err != nil {
		return nil, err
	}

	return newFrames(pcm), nil
}

// newFrames wraps a raw PCM pipe with the reusable chunk buffers.
func newFrames(pcm io.ReadCloser) *frames {
	return &frames{
		pcm:     pcm,
		raw:     make([]byte, chunkSamples*2),
		samples: make([]int16, chunkSamples),
	}
}

// frames converts the raw PCM pipe into chunks; the buffer is reused per
// the core.PCM retention contract.
type frames struct {
	pcm     io.ReadCloser
	raw     []byte
	samples []int16
	pts     time.Duration
}

// Next implements core.Frames. PTS is the arrival clock: the amount of
// audio delivered so far — for live sources the two coincide.
func (f *frames) Next(ctx context.Context) (core.PCM, error) {
	if err := ctx.Err(); err != nil {
		return core.PCM{}, errors.Join(err, f.pcm.Close())
	}

	n, err := io.ReadFull(f.pcm, f.raw)
	if n == 0 {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return core.PCM{}, errors.Join(io.EOF, f.pcm.Close())
		}

		return core.PCM{}, errors.Join(fmt.Errorf("read pcm pipe: %w", err), f.pcm.Close())
	}

	samples, decodeErr := pipeline.DecodeS16LE(f.samples, f.raw[:n-n%2])
	if decodeErr != nil {
		return core.PCM{}, errors.Join(decodeErr, f.pcm.Close())
	}

	out := core.PCM{
		Data: f.samples[:samples],
		Rate: pipeline.SampleRate,
		Ch:   1,
		PTS:  f.pts,
	}

	f.pts += time.Duration(samples) * time.Second / pipeline.SampleRate

	return out, nil
}
