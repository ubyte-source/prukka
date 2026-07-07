package pipeline_test

import (
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

func TestResampleChangesRateAndLength(t *testing.T) {
	t.Parallel()

	// 24 kHz → 16 kHz is a 2:3 decimation: 3 in, 2 out.
	in := core.PCM{Data: make([]int16, 24000), Rate: 24000, Ch: 1, PTS: time.Second}
	for i := range in.Data {
		in.Data[i] = int16(i % 1000)
	}

	out := pipeline.Resample(in, 16000)

	if out.Rate != 16000 || out.Ch != 1 {
		t.Fatalf("format = %d Hz ×%d, want 16000 ×1", out.Rate, out.Ch)
	}

	if want := 16000; len(out.Data) < want-2 || len(out.Data) > want+2 {
		t.Fatalf("length = %d, want ≈%d (1s at 16k)", len(out.Data), want)
	}

	if out.PTS != time.Second {
		t.Fatalf("PTS = %v, want carried through", out.PTS)
	}
}

func TestResampleIdentityWhenRatesMatch(t *testing.T) {
	t.Parallel()

	in := core.PCM{Data: []int16{1, 2, 3}, Rate: 16000, Ch: 1}

	out := pipeline.Resample(in, 16000)
	if out.Rate != 16000 || len(out.Data) != 3 {
		t.Fatalf("identity resample changed data: %+v", out)
	}
}

func TestResamplePreservesADCLevel(t *testing.T) {
	t.Parallel()

	// A constant signal must stay constant through linear interpolation.
	in := core.PCM{Data: make([]int16, 2400), Rate: 24000, Ch: 1}
	for i := range in.Data {
		in.Data[i] = 5000
	}

	out := pipeline.Resample(in, 16000)
	for i, s := range out.Data {
		if s != 5000 {
			t.Fatalf("sample %d = %d, want the constant 5000", i, s)
		}
	}
}

func TestResampleEmptyIsSafe(t *testing.T) {
	t.Parallel()

	out := pipeline.Resample(core.PCM{Rate: 24000, Ch: 1}, 16000)
	if out.Rate != 16000 || len(out.Data) != 0 {
		t.Fatalf("empty resample = %+v", out)
	}
}
