package ffmpeg_test

import (
	"context"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// testBinary locates an ffmpeg for live shaping tests: PRUKKA_TEST_FFMPEG
// first (local dev), then PATH (CI installs one). Absent both, skip.
func testBinary(t *testing.T) string {
	t.Helper()

	if bin := os.Getenv("PRUKKA_TEST_FFMPEG"); bin != "" {
		return bin
	}

	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg available; set PRUKKA_TEST_FFMPEG or install one")
	}

	return bin
}

func TestShapeLiveResamplesAndStretches(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))
	shaper := ffmpeg.NewShaper(sup)

	// One second of 440 Hz at 24 kHz, sped up 1.2× → ~0.833 s at 16 kHz.
	in := core.PCM{Data: make([]int16, 24000), Rate: 24000, Ch: 1}
	for i := range in.Data {
		in.Data[i] = int16(10000 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}

	out, err := shaper.Shape(t.Context(), in, 1.2, 1)
	if err != nil {
		t.Fatalf("Shape returned error: %v", err)
	}

	if out.Rate != pipeline.SampleRate || out.Ch != 1 {
		t.Fatalf("output format = %d Hz ×%d, want reference", out.Rate, out.Ch)
	}

	want := 16000.0 / 1.2
	if got := float64(len(out.Data)); got < want*0.97 || got > want*1.03 {
		t.Fatalf("output = %d samples, want ≈%.0f (1s ÷ 1.2 @16k)", len(out.Data), want)
	}

	// The tone must survive: silence would mean we shaped nothing.
	peak := int16(0)
	for _, sample := range out.Data {
		if sample > peak {
			peak = sample
		}
	}

	if peak < 5000 {
		t.Fatalf("peak = %d, want a live tone", peak)
	}
}

// TestShapeLiveMatchesRegister: 200 Hz × 1.25 must measure ≈250 Hz with
// its duration untouched.
func TestShapeLiveMatchesRegister(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))
	shaper := ffmpeg.NewShaper(sup)

	// One second of 200 Hz at 24 kHz.
	in := core.PCM{Data: make([]int16, 24000), Rate: 24000, Ch: 1}
	for i := range in.Data {
		in.Data[i] = int16(10000 * math.Sin(2*math.Pi*200*float64(i)/24000))
	}

	out, err := shaper.Shape(t.Context(), in, 1.0, 1.25)
	if err != nil {
		t.Fatalf("Shape returned error: %v", err)
	}

	if got := len(out.Data); got < 16000*97/100 || got > 16000*103/100 {
		t.Fatalf("duration changed: %d samples, want ≈16000 (the shift must be duration-neutral)", got)
	}

	f0 := pipeline.MedianF0(out.Data, out.Rate)
	if f0 < 250*0.95 || f0 > 250*1.05 {
		t.Fatalf("fundamental = %.1f Hz, want ≈250 (200 × 1.25)", f0)
	}
}

func TestShapeDeadbandSkipsFFmpeg(t *testing.T) {
	t.Parallel()

	// A nil supervisor binary would fail any subprocess; the deadband path
	// must not spawn one, proving takes that already fit avoid ffmpeg.
	shaper := ffmpeg.NewShaper(ffmpeg.NewSupervisor("/nonexistent/ffmpeg", slog.New(slog.DiscardHandler)))

	in := core.PCM{Data: make([]int16, 24000), Rate: 24000, Ch: 1}

	out, err := shaper.Shape(context.Background(), in, 1.0, 1.0)
	if err != nil {
		t.Fatalf("deadband Shape returned error (did it spawn ffmpeg?): %v", err)
	}

	if out.Rate != pipeline.SampleRate {
		t.Fatalf("deadband output rate = %d, want %d", out.Rate, pipeline.SampleRate)
	}

	if want := pipeline.SampleRate; len(out.Data) < want-2 || len(out.Data) > want+2 {
		t.Fatalf("deadband output = %d samples, want ≈%d (resampled 1s)", len(out.Data), want)
	}
}
