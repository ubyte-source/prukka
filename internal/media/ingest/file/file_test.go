package file_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ingest/file"
)

// writeWAV builds a mono WAV fixture with the given rate and sample count.
func writeWAV(t *testing.T, rate uint32, samples int) string {
	t.Helper()

	const channels = uint16(1)

	dataBytes := samples * 2
	buf := make([]byte, 44+dataBytes)
	le := binary.LittleEndian

	copy(buf[0:4], "RIFF")
	le.PutUint32(buf[4:8], uint32(36+dataBytes&0x7FFFFFFF))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	le.PutUint32(buf[16:20], 16)
	le.PutUint16(buf[20:22], 1)
	le.PutUint16(buf[22:24], channels)
	le.PutUint32(buf[24:28], rate)
	le.PutUint32(buf[28:32], rate*uint32(channels)*2)
	le.PutUint16(buf[32:34], channels*2)
	le.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	le.PutUint32(buf[40:44], uint32(dataBytes&0x7FFFFFFF))

	for i := range samples {
		le.PutUint16(buf[44+2*i:], uint16(int32(i%100)&0xFFFF))
	}

	path := filepath.Join(t.TempDir(), "probe.wav")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	return path
}

func TestOpenRejectsWrongFormats(t *testing.T) {
	t.Parallel()

	badRate := writeWAV(t, 44100, 100)

	_, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + badRate})
	if err == nil || !strings.Contains(err.Error(), "resample with") {
		t.Fatalf("wrong-rate error = %v, want a resampling hint", err)
	}

	if _, err := file.New().Open(t.Context(), core.SourceSpec{URL: "rtmp://x"}); err == nil {
		t.Fatal("non-file URL accepted")
	}

	if _, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file:///does/not/exist.wav"}); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestFramesDeliverPacedPCMUntilEOF(t *testing.T) {
	t.Parallel()

	// Half a second of audio: five 100 ms chunks, then EOF.
	path := writeWAV(t, pipeline.SampleRate, pipeline.SampleRate/2)

	frames, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	start := time.Now()
	total, chunks := drain(t, frames)

	if total != pipeline.SampleRate/2 || chunks != 5 {
		t.Fatalf("delivered %d samples in %d chunks, want %d in 5", total, chunks, pipeline.SampleRate/2)
	}

	// Real-time pacing: 500 ms of audio must take roughly that long.
	if elapsed := time.Since(start); elapsed < 350*time.Millisecond {
		t.Fatalf("playback took %v, want real-time pacing", elapsed)
	}
}

func TestPCMQuantumControlsDeliverySizeAndPTS(t *testing.T) {
	t.Parallel()

	const quantum = 20 * time.Millisecond
	path := writeWAV(t, pipeline.SampleRate, pipeline.SampleRate/25)

	frames, err := file.New(file.WithPCMQuantum(quantum)).Open(
		t.Context(),
		core.SourceSpec{URL: "file://" + path},
	)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	first, err := frames.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next returned error: %v", err)
	}
	if len(first.Data) != pipeline.SampleRate/50 || first.PTS != 0 {
		t.Fatalf("first delivery = %d samples @ %v, want 20 ms @ 0", len(first.Data), first.PTS)
	}

	second, err := frames.Next(t.Context())
	if err != nil {
		t.Fatalf("second Next returned error: %v", err)
	}
	if len(second.Data) != pipeline.SampleRate/50 || second.PTS != quantum {
		t.Fatalf("second delivery = %d samples @ %v, want 20 ms @ %v", len(second.Data), second.PTS, quantum)
	}
	if _, err := frames.Next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal Next error = %v, want io.EOF", err)
	}
}

func TestPCMQuantumRejectsInvalidDurations(t *testing.T) {
	t.Parallel()

	for name, quantum := range map[string]time.Duration{
		"negative":    -time.Millisecond,
		"not aligned": time.Second/pipeline.SampleRate + time.Nanosecond,
		"zero":        0,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatalf("WithPCMQuantum(%v) did not panic", quantum)
				}
			}()

			_ = file.WithPCMQuantum(quantum)
		})
	}
}

// drain consumes frames until EOF, checking format and PTS continuity.
func drain(t *testing.T, frames core.Frames) (total, chunks int) {
	t.Helper()

	for {
		chunk, nextErr := frames.Next(t.Context())
		if errors.Is(nextErr, io.EOF) {
			return total, chunks
		}

		if nextErr != nil {
			t.Fatalf("Next returned error: %v", nextErr)
		}

		if chunk.Rate != pipeline.SampleRate || chunk.Ch != 1 {
			t.Fatalf("chunk format = %d Hz × %d ch", chunk.Rate, chunk.Ch)
		}

		if want := time.Duration(total) * time.Second / pipeline.SampleRate; chunk.PTS != want {
			t.Fatalf("chunk PTS = %v, want %v", chunk.PTS, want)
		}

		total += len(chunk.Data)
		chunks++
	}
}

func TestLoopKeepsPTSMonotonic(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, pipeline.SampleRate/10)

	frames, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path + "?loop=true"})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	var last time.Duration

	// Three passes over a 100 ms file: PTS must never rewind.
	for range 3 {
		chunk, nextErr := frames.Next(t.Context())
		if nextErr != nil {
			t.Fatalf("Next returned error: %v", nextErr)
		}

		if chunk.PTS < last {
			t.Fatalf("PTS rewound: %v after %v", chunk.PTS, last)
		}

		last = chunk.PTS
	}

	// A looped source never hits EOF: cancellation is what releases the
	// file, and Windows cannot delete it while it stays open.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := frames.Next(canceled); err == nil {
		t.Fatal("canceled Next kept the looped source open")
	}
}

func TestLoopRejectsEmptyInput(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, 0)

	_, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path + "?loop=true"})
	if err == nil || !strings.Contains(err.Error(), "empty data") {
		t.Fatalf("Open error = %v, want empty-data rejection", err)
	}
}

func TestShortLoopAlwaysAdvances(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, 1)
	frames, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path + "?loop=true"})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	first, err := frames.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next returned error: %v", err)
	}
	second, err := frames.Next(t.Context())
	if err != nil {
		t.Fatalf("second Next returned error: %v", err)
	}
	if len(first.Data) != 1 || len(second.Data) != 1 || second.PTS <= first.PTS {
		t.Fatalf("loop frames = (%d @ %v, %d @ %v), want progressing single samples",
			len(first.Data), first.PTS, len(second.Data), second.PTS)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := frames.Next(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Next error = %v, want context.Canceled", err)
	}
}

func TestLoopStopsWhenSourceCannotProgress(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, pipeline.SampleRate/10)
	frames, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path + "?loop=true"})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if err := os.Truncate(path, 44); err != nil {
		t.Fatalf("truncate source: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	if _, err := frames.Next(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next error = %v, want immediate truncated-source error", err)
	}
}

func TestOpenRejectsAmbiguousOptionsAndNonFiles(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, 1)
	for _, suffix := range []string{"?loop=yes", "?loop=true&loop=false", "?other=true"} {
		if _, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path + suffix}); err == nil {
			t.Fatalf("source option %q accepted", suffix)
		}
	}

	if _, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + t.TempDir()}); err == nil {
		t.Fatal("directory source accepted")
	}
}

func TestNextHonorsCancellation(t *testing.T) {
	t.Parallel()

	path := writeWAV(t, pipeline.SampleRate, pipeline.SampleRate)

	frames, err := file.New().Open(t.Context(), core.SourceSpec{URL: "file://" + path})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, nextErr := frames.Next(ctx); !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("Next error = %v, want context.Canceled", nextErr)
	}
}
