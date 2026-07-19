package speechengine

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodePCM16WAV(t *testing.T) {
	t.Parallel()

	want := []int16{-32768, -1, 0, 1, 32767}
	got, rate, err := decodePCM16WAV(encodeWAV(want, 22050))
	if err != nil || rate != 22050 || !reflect.DeepEqual(got, want) {
		t.Fatalf("decodePCM16WAV = %v, %d, %v", got, rate, err)
	}

	truncated := encodeWAV(want, 22050)[:44]
	if _, _, err := decodePCM16WAV(truncated); !errors.Is(err, errIncompleteWAV) {
		t.Fatalf("truncated error = %v, want incomplete", err)
	}

	invalid := encodeWAV(want, 22050)
	binary.LittleEndian.PutUint16(invalid[22:24], 2)
	if _, _, err := decodePCM16WAV(invalid); err == nil {
		t.Fatal("stereo WAV succeeded")
	}
}

// TestReadPCM16WAVEnforcesTheOutputSizeCap: ttsWAVMaxBytes is the last bound
// on what one Piper take may hand back before the reader swallows it whole.
// Its sibling caps — the TTS text cap, the archive caps — are all pinned by
// tests; this one pins the WAV cap: one byte over fails naming the cap
// before any read, and a cap-sized file passes the size gate to fail as a
// malformed WAV, not as an oversized one.
func TestReadPCM16WAVEnforcesTheOutputSizeCap(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "take.wav")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("reserve output: %v", err)
	}
	if err := os.Truncate(path, ttsWAVMaxBytes+1); err != nil {
		t.Fatalf("grow past the cap: %v", err)
	}

	_, _, err := readPCM16WAV(path)
	if err == nil || !strings.Contains(err.Error(), "WAV exceeds") {
		t.Fatalf("oversized WAV = %v, want the size cap named", err)
	}

	if err = os.Truncate(path, ttsWAVMaxBytes); err != nil {
		t.Fatalf("shrink to the cap: %v", err)
	}
	_, _, err = readPCM16WAV(path)
	if err == nil || strings.Contains(err.Error(), "WAV exceeds") {
		t.Fatalf("cap-sized WAV = %v, want it admitted past the size gate", err)
	}
}

func TestResamplePCMInterpolatesWhenTheRateRises(t *testing.T) {
	t.Parallel()

	in := []int16{0, 1000, 2000, 3000}
	if got := resamplePCM(in, 16000, 16000); &got[0] != &in[0] {
		t.Fatal("same-rate resample copied its input")
	}

	// A rising rate cannot fold, but the reconstruction is still band-limited:
	// doubling the rate must keep the tone and suppress its mirror image at
	// from-freq, which linear interpolation leaves only ~28 dB down.
	const (
		from         = 8000
		to           = 16000
		freq         = 1000.0
		mirror       = float64(from) - freq
		wantMirrorDB = 60.0
	)

	up := resamplePCM(tonePCM16(from, freq), from, to)
	if len(up) != to {
		t.Fatalf("upsampled length = %d, want %d", len(up), to)
	}
	kept := goertzelMagnitude(up, to, freq)
	if rejection := 20 * math.Log10(kept/goertzelMagnitude(up, to, mirror)); rejection < wantMirrorDB {
		t.Errorf("mirror rejection = %.1f dB at %.0f Hz, want at least %.1f dB",
			rejection, mirror, wantMirrorDB)
	}

	source := goertzelMagnitude(tonePCM16(from, freq), from, freq)
	if moved := 20 * math.Log10(source/kept*float64(to)/float64(from)); math.Abs(moved) > 1 {
		t.Errorf("upsampled level moved by %.1f dB at %.0f Hz", moved, freq)
	}
}

// TestResamplePCMAttenuatesOutOfBandEnergyWhenDecimating measures the case
// every dubbed word takes: 22.05 kHz Piper output down to the 16 kHz
// reference rate. Content above the new 8 kHz Nyquist must be attenuated, not
// mirrored back into the 5–8 kHz band where sibilants live. Bare linear
// interpolation leaves a 9 kHz tone only ~5 dB down at its 7 kHz alias.
func TestResamplePCMAttenuatesOutOfBandEnergyWhenDecimating(t *testing.T) {
	t.Parallel()

	const (
		from    = 22050
		to      = 16000
		inBand  = 1000.0
		aliased = 9000.0
		// 9 kHz sampled at 16 kHz reappears at |16000 - 9000| = 7 kHz.
		alias = float64(to) - aliased
		// A Blackman-windowed sinc clears this by a wide margin; bare linear
		// interpolation is 30 dB short of it.
		wantRejectionDB = 40.0
	)

	reference := goertzelMagnitude(resamplePCM(tonePCM16(from, inBand), from, to), to, inBand)
	folded := goertzelMagnitude(resamplePCM(tonePCM16(from, aliased), from, to), to, alias)

	rejection := 20 * math.Log10(reference/folded)
	if rejection < wantRejectionDB {
		t.Fatalf("out-of-band rejection = %.1f dB, want at least %.1f dB: "+
			"a %.0f Hz tone folded back to %.0f Hz", rejection, wantRejectionDB, aliased, alias)
	}

	// The in-band tone must survive the filter, not be traded away for the
	// stopband: compare it against the same tone before resampling.
	source := goertzelMagnitude(tonePCM16(from, inBand), from, inBand)
	if loss := 20 * math.Log10(source/reference*float64(to)/float64(from)); loss > 1 {
		t.Fatalf("in-band loss = %.1f dB at %.0f Hz, want the passband intact", loss, inBand)
	}
}

// TestResamplePCMDoesNotFoldPassbandEnergyWhenDecimating measures the fold the
// stopband test above cannot see. The energy landing in the low band does not
// arrive from above the target's Nyquist: it is the input's OWN first image at
// from-freq, which no anti-alias filter reaches, because the content producing
// it sits in that filter's PASSBAND. Re-spacing the samples with a 2-tap
// interpolator leaves that image ~13 dB down and folds it into the audible
// band: 6 kHz of a 22.05 kHz Piper take reappears at 50 Hz, 7 kHz at 950 Hz —
// the buzz under every /s/, /ʃ/ and /f/.
func TestResamplePCMDoesNotFoldPassbandEnergyWhenDecimating(t *testing.T) {
	t.Parallel()

	const (
		from = 22050
		to   = 16000
		// One band-limited kernel clears 100 dB here; a low-pass followed by a
		// 2-tap interpolator manages 19.
		wantRejectionDB = 80.0
	)

	reference := steadyStateMagnitude(resamplePCM(tonePCM16(from, 1000), from, to), to, 1000)
	for _, fold := range []struct{ tone, image float64 }{
		{tone: 6000, image: 50},  // 22050-6000 = 16050 Hz, which 16 kHz sampling folds to 50 Hz.
		{tone: 7000, image: 950}, // 22050-7000 = 15050 Hz folds to 950 Hz.
	} {
		folded := steadyStateMagnitude(resamplePCM(tonePCM16(from, fold.tone), from, to), to, fold.image)
		if rejection := 20 * math.Log10(reference/folded); rejection < wantRejectionDB {
			t.Errorf("passband fold rejection = %.1f dB, want at least %.1f dB: "+
				"a %.0f Hz tone reappeared at %.0f Hz", rejection, wantRejectionDB, fold.tone, fold.image)
		}
	}
}

// steadyStateMagnitude measures one bin over the middle half of a one-second
// take. The leading and trailing kernel half-widths carry the edge-hold
// extension — a deliberate boundary behavior, not aliasing — and its spectral
// splatter sits ~28 dB above the fold being measured. Half a second at 16 kHz
// keeps every 2 Hz multiple an exact bin, so the surviving tone cannot leak
// into the image's bin either.
func steadyStateMagnitude(pcm []int16, rate int, freq float64) float64 {
	return goertzelMagnitude(pcm[len(pcm)/4:len(pcm)*3/4], rate, freq)
}

// tonePCM16 is one second of a full-ish-scale sine at the given rate.
func tonePCM16(rate int, freq float64) []int16 {
	out := make([]int16, rate)
	for i := range out {
		out[i] = int16(16000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}

	return out
}

// goertzelMagnitude returns the energy of one frequency bin, which is all the
// filter assertions need from a spectrum.
func goertzelMagnitude(samples []int16, rate int, freq float64) float64 {
	angle := 2 * math.Pi * freq / float64(rate)
	coeff := 2 * math.Cos(angle)
	var previous, older float64
	for _, sample := range samples {
		previous, older = float64(sample)+coeff*previous-older, previous
	}

	return math.Hypot(previous-older*math.Cos(angle), older*math.Sin(angle))
}

func TestPCMConversionRoundTrip(t *testing.T) {
	t.Parallel()

	want := []int16{-32768, -1, 0, 1, 32767}
	if got := bytesToInt16(int16ToBytes(want)); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
}

func TestEncodeWAVHeader(t *testing.T) {
	t.Parallel()

	pcm := []int16{1, -1, 2}
	wav := encodeWAV(pcm, 16000)
	if len(wav) != 44+len(pcm)*2 {
		t.Fatalf("wav length = %d", len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("invalid WAV markers: %q/%q/%q", wav[:4], wav[8:12], wav[36:40])
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != 6 {
		t.Fatalf("data length = %d", got)
	}
}
