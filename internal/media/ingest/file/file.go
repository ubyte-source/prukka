// Package file implements the file:// ingress: a native WAV reader that
// paces playback at real time to simulate a live source, no ffmpeg involved.
package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// DefaultPCMQuantum is the amount of reference audio delivered by one call to
// Frames.Next when the ingress has no profile-specific override.
const DefaultPCMQuantum = 100 * time.Millisecond

// Ingress opens file:// sources.
type Ingress struct {
	quantumSamples int
}

// Option configures an ingress at construction time.
type Option func(*Ingress)

// WithPCMQuantum sets the amount of decoded audio returned by each successful
// Frames.Next call. The quantum must be positive and contain a whole number of
// reference-rate samples.
func WithPCMQuantum(quantum time.Duration) Option {
	samples := pipeline.SamplesInQuantum(quantum)

	return func(ingress *Ingress) { ingress.quantumSamples = samples }
}

// Compile-time port check.
var _ core.Ingress = Ingress{}

// New returns the file ingress.
func New(options ...Option) Ingress {
	ingress := Ingress{quantumSamples: pipeline.SamplesInQuantum(DefaultPCMQuantum)}
	for _, option := range options {
		option(&ingress)
	}

	return ingress
}

// Open implements core.Ingress: the source must be a 16 kHz mono 16-bit
// WAV; `?loop=true` restarts at EOF.
func (i Ingress) Open(ctx context.Context, src core.SourceSpec) (core.Frames, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("file ingress: %w", err)
	}

	path, query, err := sourcePath(src.URL)
	if err != nil {
		return nil, err
	}

	loop, err := parseLoop(query)
	if err != nil {
		return nil, fmt.Errorf("file ingress: %w", err)
	}

	input, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("file ingress: %w", err)
	}

	info, err := input.Stat()
	if err != nil {
		return nil, closeInput(input, fmt.Errorf("file ingress %s: stat: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return nil, closeInput(input, fmt.Errorf("file ingress %s: source is not a regular file", path))
	}

	spec, err := inspectWAV(input, info.Size())
	if err != nil {
		return nil, closeInput(input, fmt.Errorf("file ingress %s: %w", path, err))
	}

	return &frames{
		input:      input,
		dataOffset: spec.dataOffset,
		dataBytes:  spec.dataBytes,
		start:      time.Now(),
		loop:       loop,
		raw:        make([]byte, i.quantumSamples*2),
		samples:    make([]int16, i.quantumSamples),
	}, nil
}

func closeInput(input *os.File, cause error) error {
	if err := input.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close WAV source: %w", err))
	}

	return cause
}

func parseLoop(query url.Values) (bool, error) {
	for key := range query {
		if key != "loop" {
			return false, fmt.Errorf("unsupported query parameter %q", key)
		}
	}

	values, ok := query["loop"]
	if !ok {
		return false, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return false, errors.New("loop must be exactly true or false")
	}

	return values[0] == "true", nil
}

// sourcePath splits a file:// URL by hand: url.Parse rejects Windows forms
// like file://C:\in.wav that users naturally type.
func sourcePath(rawURL string) (string, url.Values, error) {
	trimmed, ok := strings.CutPrefix(rawURL, "file://")
	if !ok {
		return "", nil, fmt.Errorf("file ingress: unsupported source %q", rawURL)
	}

	trimmed, rawQuery, _ := strings.Cut(trimmed, "?")

	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, fmt.Errorf("file ingress: unsupported source %q", rawURL)
	}

	if unescaped, unescErr := url.PathUnescape(trimmed); unescErr == nil {
		trimmed = unescaped
	}

	// file:///C:/in.wav carries the drive after a leading slash; drop it so
	// the path opens natively on Windows.
	if len(trimmed) > 1 && trimmed[0] == '/' && filepath.VolumeName(trimmed[1:]) != "" {
		trimmed = trimmed[1:]
	}

	return filepath.Clean(filepath.FromSlash(trimmed)), query, nil
}

// frames reads PCM chunks at real-time pace.
type frames struct {
	input      *os.File
	start      time.Time
	closeErr   error
	raw        []byte
	samples    []int16
	closeOnce  sync.Once
	dataOffset int64
	dataBytes  int64
	pos        int64
	pts        time.Duration
	closed     atomic.Bool
	loop       bool
}

// Next implements core.Frames: it blocks until the next chunk is due on the
// wall clock, so downstream latency measurements mean something.
func (f *frames) Next(ctx context.Context) (core.PCM, error) {
	if f.closed.Load() {
		return core.PCM{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return f.stop(err)
	}
	if f.pos >= f.dataBytes {
		if !f.loop {
			return f.stop(io.EOF)
		}

		// Looping keeps PTS monotonic: the file restarts, the clock does
		// not.
		f.pos = 0
	}

	if err := f.pace(ctx); err != nil {
		return f.stop(err)
	}

	samples := min(int64(len(f.samples)), (f.dataBytes-f.pos)/2)
	raw := f.raw[:samples*2]
	if err := readAt(f.input, raw, f.dataOffset+f.pos); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return f.stop(ctxErr)
		}

		return f.stop(fmt.Errorf("read WAV data: %w", err))
	}

	decoded := pipeline.DecodeS16LE(f.samples[:samples], raw)

	out := core.PCM{
		Data: f.samples[:decoded],
		Rate: pipeline.SampleRate,
		Ch:   1,
		PTS:  f.pts,
	}

	f.pts += time.Duration(samples) * time.Second / pipeline.SampleRate
	f.pos += samples * 2

	return out, nil
}

func (f *frames) stop(cause error) (core.PCM, error) {
	if err := f.Close(); err != nil {
		cause = errors.Join(cause, fmt.Errorf("close WAV source: %w", err))
	}

	return core.PCM{}, cause
}

// Close releases the WAV handle. It is safe to race with cancellation cleanup
// and may be called repeatedly.
func (f *frames) Close() error {
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		f.closeErr = f.input.Close()
	})

	return f.closeErr
}

// pace sleeps until this chunk's wall-clock due time.
func (f *frames) pace(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

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
