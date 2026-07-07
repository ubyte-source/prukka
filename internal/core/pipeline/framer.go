// Package pipeline hosts the real-time audio stages: framing, endpointing,
// speaker clustering, scheduling, isochrony, ducking. No per-frame allocs.
package pipeline

import (
	"errors"
	"fmt"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// SampleRate is the internal reference sample rate in Hz.
const SampleRate = 16000

// FrameDuration is the internal framing interval.
const FrameDuration = 20 * time.Millisecond

// FrameSamples is the number of mono samples in one internal frame.
const FrameSamples = int(SampleRate * FrameDuration / time.Second)

// ErrBadFormat marks input that is not reference-format PCM; conversion is
// the ingest adapter's job.
var ErrBadFormat = errors.New("pcm is not 16 kHz mono")

// Framer re-slices arbitrary PCM chunks into fixed 20 ms frames; emitted
// frames reuse storage (copy to retain) and Push never allocates.
type Framer struct {
	pending []int16       // fixed storage for one partial frame
	n       int           // samples currently pending
	pts     time.Duration // source PTS of pending[0], valid when n > 0
}

// NewFramer returns a framer with its single frame buffer preallocated.
func NewFramer() *Framer {
	return &Framer{pending: make([]int16, FrameSamples)}
}

// Push consumes one chunk and invokes emit once per completed frame, keeping
// source PTS continuous across chunk boundaries.
func (f *Framer) Push(chunk core.PCM, emit func(core.PCM)) error {
	if chunk.Rate != SampleRate || chunk.Ch != 1 {
		return fmt.Errorf("%w: got %d Hz × %d ch", ErrBadFormat, chunk.Rate, chunk.Ch)
	}

	data := chunk.Data
	pts := chunk.PTS

	if f.n > 0 {
		take := min(FrameSamples-f.n, len(data))
		copy(f.pending[f.n:], data[:take])
		f.n += take
		data = data[take:]
		pts += samplesDuration(take)

		if f.n < FrameSamples {
			return nil
		}

		emit(core.PCM{Data: f.pending, Rate: SampleRate, Ch: 1, PTS: f.pts})
		f.n = 0
	}

	for len(data) >= FrameSamples {
		// Full frames alias the caller's chunk storage — zero copy; the
		// three-index slice keeps consumers from appending into it.
		emit(core.PCM{Data: data[:FrameSamples:FrameSamples], Rate: SampleRate, Ch: 1, PTS: pts})

		data = data[FrameSamples:]
		pts += FrameDuration
	}

	if len(data) > 0 {
		copy(f.pending, data)
		f.n = len(data)
		f.pts = pts
	}

	return nil
}

// samplesDuration converts a sample count to time at the reference rate.
func samplesDuration(n int) time.Duration {
	return time.Duration(n) * time.Second / SampleRate
}
