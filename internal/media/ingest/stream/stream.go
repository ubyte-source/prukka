// Package stream implements the rtmp:// and srt:// ingresses over the
// supervised ffmpeg demuxer.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// chunkSamples sizes one Next delivery: 100 ms of reference audio.
const chunkSamples = pipeline.SampleRate / 10

var errTruncatedPCM = errors.New("truncated 16-bit PCM sample")

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
	pcm         io.ReadCloser
	pendingErr  error
	terminalErr error
	closeErr    error
	raw         []byte
	samples     []int16
	pts         time.Duration
	closeOnce   sync.Once
	closed      atomic.Bool
	done        bool
}

// Next implements core.Frames. PTS is the arrival clock: the amount of
// audio delivered so far — for live sources the two coincide.
func (f *frames) Next(ctx context.Context) (core.PCM, error) {
	if f.done {
		return core.PCM{}, f.terminalErr
	}
	if f.pendingErr != nil {
		readErr := f.pendingErr
		f.pendingErr = nil

		return f.finish(ctx, readErr)
	}
	if err := ctx.Err(); err != nil {
		return f.end(errors.Join(err, f.Close()))
	}
	if f.closed.Load() {
		return f.end(errors.Join(io.ErrClosedPipe, f.Close()))
	}

	stopCancelClose := context.AfterFunc(ctx, f.closeForCancel)
	n, err := io.ReadFull(f.pcm, f.raw)
	if !stopCancelClose() {
		// Wait for an already-running cancellation callback before reading its
		// cached close result.
		f.closeForCancel()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return f.end(errors.Join(ctxErr, f.Close()))
	}
	if n == 0 {
		return f.finish(ctx, err)
	}
	usable := f.deferReadError(n, err)
	if usable == 0 {
		readErr := f.pendingErr
		f.pendingErr = nil

		return f.finish(ctx, readErr)
	}

	samples := pipeline.DecodeS16LE(f.samples, f.raw[:usable])

	out := core.PCM{
		Data: f.samples[:samples],
		Rate: pipeline.SampleRate,
		Ch:   1,
		PTS:  f.pts,
	}

	f.pts += time.Duration(samples) * time.Second / pipeline.SampleRate

	return out, nil
}

func (f *frames) deferReadError(n int, readErr error) int {
	if n%2 != 0 {
		f.pendingErr = errTruncatedPCM
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			f.pendingErr = errors.Join(readErr, errTruncatedPCM)
		}
	} else if readErr != nil {
		// A Reader may return data and a terminal error together. Deliver the
		// data now, then surface the error without reading the source again.
		f.pendingErr = readErr
	}

	return n - n%2
}

func (f *frames) finish(ctx context.Context, readErr error) (core.PCM, error) {
	closeErr := f.Close()
	if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return f.end(errors.Join(ctx.Err(), fmt.Errorf("read pcm pipe: %w", readErr), closeErr))
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return f.end(errors.Join(ctxErr, closeErr))
	}
	if closeErr != nil {
		return f.end(fmt.Errorf("pcm source exited: %w", closeErr))
	}

	return f.end(io.EOF)
}

func (f *frames) closeForCancel() {
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		f.closeErr = f.pcm.Close()
	})
}

// Close interrupts a blocked pipe read and reaps the supervised source. It is
// safe for Next cancellation and the engine owner to call concurrently.
func (f *frames) Close() error {
	f.closeForCancel()

	return f.closeErr
}

func (f *frames) end(err error) (core.PCM, error) {
	f.done = true
	f.terminalErr = err

	return core.PCM{}, err
}
