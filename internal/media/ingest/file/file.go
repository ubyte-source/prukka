// Package file implements the file:// ingress: a native WAV reader that
// paces playback at real time to simulate a live source, no ffmpeg involved.
package file

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// chunk is how much audio one Next delivers; 100 ms keeps pacing smooth
// without per-frame wakeups.
const chunk = 100 * time.Millisecond

// chunkSamples is the sample count behind one chunk.
const chunkSamples = int(pipeline.SampleRate * chunk / time.Second)

// Ingress opens file:// sources.
type Ingress struct{}

// Compile-time port check.
var _ core.Ingress = Ingress{}

// New returns the file ingress.
func New() Ingress {
	return Ingress{}
}

// Open implements core.Ingress: the source must be a 16 kHz mono 16-bit
// WAV; `?loop=true` restarts at EOF.
func (Ingress) Open(_ context.Context, src core.SourceSpec) (core.Frames, error) {
	u, err := url.Parse(src.URL)
	if err != nil || u.Scheme != "file" {
		return nil, fmt.Errorf("file ingress: unsupported source %q", src.URL)
	}

	path := filepath.Clean(u.Path)

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, fmt.Errorf("file ingress: %w", readErr)
	}

	samples, parseErr := parseWAV(data)
	if parseErr != nil {
		return nil, fmt.Errorf("file ingress %s: %w", path, parseErr)
	}

	return &frames{
		samples: samples,
		start:   time.Now(),
		loop:    u.Query().Get("loop") == "true",
	}, nil
}

// frames replays decoded samples at real-time pace.
type frames struct {
	start   time.Time
	samples []int16
	pos     int
	pts     time.Duration
	loop    bool
}

// Next implements core.Frames: it blocks until the next chunk is due on the
// wall clock, so downstream latency measurements mean something.
func (f *frames) Next(ctx context.Context) (core.PCM, error) {
	if f.pos >= len(f.samples) {
		if !f.loop {
			return core.PCM{}, io.EOF
		}

		// Looping keeps PTS monotonic: the file restarts, the clock does
		// not.
		f.pos = 0
	}

	if err := f.pace(ctx); err != nil {
		return core.PCM{}, err
	}

	end := min(f.pos+chunkSamples, len(f.samples))
	out := core.PCM{
		Data: f.samples[f.pos:end],
		Rate: pipeline.SampleRate,
		Ch:   1,
		PTS:  f.pts,
	}

	f.pts += time.Duration(end-f.pos) * time.Second / pipeline.SampleRate
	f.pos = end

	return out, nil
}

// pace sleeps until this chunk's wall-clock due time.
func (f *frames) pace(ctx context.Context) error {
	wait := time.Until(f.start.Add(f.pts))
	if wait <= 0 {
		return nil
	}

	t := time.NewTimer(wait)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
