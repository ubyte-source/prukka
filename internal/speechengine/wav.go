package speechengine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"time"
)

var errIncompleteWAV = errors.New("incomplete WAV")

func waitForPCM16WAV(path string, window time.Duration) (samples []int16, rate int, err error) {
	deadline := time.Now().Add(window)
	for {
		samples, rate, err = readPCM16WAV(path)
		if err == nil {
			return samples, rate, nil
		}
		if !errors.Is(err, errIncompleteWAV) {
			return nil, 0, err
		}
		if time.Now().After(deadline) {
			return nil, 0, fmt.Errorf("wait for completed output: %w", err)
		}

		time.Sleep(time.Millisecond)
	}
}

func readPCM16WAV(path string) (samples []int16, rate int, err error) {
	file, openErr := openCompletePCM16WAV(path)
	if openErr != nil {
		return nil, 0, openErr
	}

	raw, readErr := io.ReadAll(file)
	err = errors.Join(readErr, file.Close())
	if err != nil {
		return nil, 0, err
	}

	return decodePCM16WAV(raw)
}

//nolint:gosec // The path is a private temporary output reserved by the caller.
func openCompletePCM16WAV(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errIncompleteWAV
		}

		return nil, err
	}
	if err := validatePCM16WAVFile(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return file, nil
}

func validatePCM16WAVFile(file *os.File) error {
	info, statErr := file.Stat()
	if statErr != nil {
		return statErr
	}
	if info.Size() < 12 {
		return errIncompleteWAV
	}
	if info.Size() > ttsWAVMaxBytes {
		return fmt.Errorf("WAV exceeds %d bytes", ttsWAVMaxBytes)
	}

	header := make([]byte, 12)
	if _, readErr := io.ReadFull(file, header); readErr != nil {
		return errors.Join(errIncompleteWAV, readErr)
	}
	if err := checkRIFFEnvelope(header, info.Size()); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	return nil
}

// checkRIFFEnvelope validates the 12-byte RIFF/WAVE header against the source's
// total size: a still-growing file reads as incomplete, a mismatched declared
// size as corrupt.
func checkRIFFEnvelope(header []byte, totalSize int64) error {
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return errors.New("WAV has invalid RIFF/WAVE signature")
	}
	wantSize := int64(binary.LittleEndian.Uint32(header[4:8])) + 8
	if wantSize > totalSize {
		return errIncompleteWAV
	}
	if wantSize != totalSize {
		return fmt.Errorf("WAV RIFF size %d does not match file size %d", wantSize, totalSize)
	}

	return nil
}

func decodePCM16WAV(raw []byte) (samples []int16, rate int, err error) {
	if envelopeErr := validatePCM16WAVEnvelope(raw); envelopeErr != nil {
		return nil, 0, envelopeErr
	}

	data, rate, err := scanPCM16WAVChunks(raw)
	if err != nil {
		return nil, 0, err
	}
	if len(data)%2 != 0 {
		return nil, 0, errors.New("WAV PCM data has an odd byte count")
	}

	return bytesToInt16(data), rate, nil
}

func validatePCM16WAVEnvelope(raw []byte) error {
	if len(raw) < 12 {
		return errIncompleteWAV
	}

	return checkRIFFEnvelope(raw[:12], int64(len(raw)))
}

func scanPCM16WAVChunks(raw []byte) (data []byte, rate int, err error) {
	var chunks pcm16WAVChunks
	for offset := 12; offset < len(raw); {
		chunkID, chunk, next, err := readWAVChunk(raw, offset)
		if err != nil {
			return nil, 0, err
		}
		if addErr := chunks.add(chunkID, chunk); addErr != nil {
			return nil, 0, addErr
		}
		offset = next
	}

	return chunks.result()
}

type pcm16WAVChunks struct {
	data       []byte
	rate       int
	formatSeen bool
}

func (c *pcm16WAVChunks) add(chunkID string, chunk []byte) error {
	switch chunkID {
	case "fmt ":
		if c.formatSeen {
			return errors.New("WAV has duplicate fmt chunks")
		}
		rate, err := parsePCM16WAVFormat(chunk)
		if err != nil {
			return err
		}
		c.rate, c.formatSeen = rate, true
	case "data":
		if c.data != nil {
			return errors.New("WAV has duplicate data chunks")
		}
		if len(chunk) == 0 {
			return errors.New("WAV PCM data is empty")
		}
		c.data = chunk
	}

	return nil
}

func (c *pcm16WAVChunks) result() (data []byte, rate int, err error) {
	if !c.formatSeen || c.data == nil {
		return nil, 0, errors.New("WAV requires fmt and data chunks")
	}

	return c.data, c.rate, nil
}

//nolint:gosec // Length checks bound the WAV chunk size before converting it to int.
func readWAVChunk(raw []byte, offset int) (chunkID string, chunk []byte, next int, err error) {
	if len(raw)-offset < 8 {
		return "", nil, 0, errors.New("WAV has a truncated chunk header")
	}
	chunkID = string(raw[offset : offset+4])
	chunkSize := uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
	start := offset + 8
	if chunkSize > uint64(len(raw)-start) {
		return "", nil, 0, errIncompleteWAV
	}
	end := start + int(chunkSize)
	next = end + int(chunkSize%2)
	if next > len(raw) {
		return "", nil, 0, errors.New("WAV is missing chunk padding")
	}

	return chunkID, raw[start:end], next, nil
}

func parsePCM16WAVFormat(chunk []byte) (int, error) {
	if len(chunk) < 16 {
		return 0, errors.New("WAV fmt chunk is shorter than 16 bytes")
	}
	format := binary.LittleEndian.Uint16(chunk[0:2])
	channels := binary.LittleEndian.Uint16(chunk[2:4])
	sampleRate := binary.LittleEndian.Uint32(chunk[4:8])
	byteRate := binary.LittleEndian.Uint32(chunk[8:12])
	blockAlign := binary.LittleEndian.Uint16(chunk[12:14])
	bits := binary.LittleEndian.Uint16(chunk[14:16])
	if format != 1 || channels != 1 || bits != 16 || blockAlign != 2 ||
		sampleRate == 0 || sampleRate > 384000 || byteRate != sampleRate*2 {
		return 0, fmt.Errorf(
			"WAV format=%d channels=%d rate=%d byte_rate=%d block_align=%d bits=%d; "+
				"want PCM mono 16-bit",
			format, channels, sampleRate, byteRate, blockAlign, bits,
		)
	}

	return int(sampleRate), nil
}

// resampleTaps is the kernel length: 63 taps of a Blackman-windowed sinc place
// the first fold frequency deep in the stopband.
const resampleTaps = 63

// resampleCenter is the tap the kernel is centered on, so the symmetric FIR's
// constant group delay is removed rather than heard as a shift.
const resampleCenter = (resampleTaps - 1) / 2

// resampleCutoff is the low-pass corner as a fraction of the LOWER of the two
// rates: 0.45 leaves the transition band inside that rate's own Nyquist.
const resampleCutoff = 0.45

// resamplePCM resamples 16-bit PCM between rates with a single band-limited
// kernel, evaluated once per OUTPUT sample at that sample's exact instant on
// the input timeline. Band-limiting and re-spacing MUST be the same operation: a
// low-pass plus a 2-tap linear interpolator leaves the first image at from-freq
// only ~13 dB down, inside the PASSBAND no stopband attenuation reaches, and
// re-spacing folds 7 kHz of a 22.05 kHz take to 950 Hz and 6 kHz to 50 Hz. It
// runs once per synthesized take, beside the Piper inference that produced it
// and never per frame, which is what licenses a kernel this expensive.
func resamplePCM(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return in
	}

	kernel := newResampleKernel(from, to)
	out := make([]int16, len(in)*to/from)
	// Exact integers rather than a float position: a long take cannot drift.
	base, phase := 0, 0
	for i := range out {
		out[i] = kernel.at(in, base, phase)
		phase += kernel.step
		base += phase / kernel.phases
		phase %= kernel.phases
	}

	return out
}

// resampleKernel holds one FIR row per fractional offset the conversion can
// land on. Output sample i sits at input instant i*step/phases.
type resampleKernel struct {
	rows   []float64 // phases rows of resampleTaps, laid out flat.
	step   int
	phases int
}

// newResampleKernel builds every phase the conversion needs: from/to is
// rational, so reducing by the GCD makes the set exact and finite — 22050/16000
// reduces to 441/320, hence 320 rows.
func newResampleKernel(from, to int) resampleKernel {
	divisor := gcd(from, to)
	phases := to / divisor
	kernel := resampleKernel{
		rows:   make([]float64, phases*resampleTaps),
		step:   from / divisor,
		phases: phases,
	}
	// The corner is in INPUT cycles per sample: a rate reduction must band-limit
	// to the TARGET's Nyquist, not the source's.
	cutoff := resampleCutoff * min(1, float64(to)/float64(from))
	for phase := range phases {
		fillSincRow(kernel.row(phase), cutoff, float64(phase)/float64(phases))
	}

	return kernel
}

func (k resampleKernel) row(phase int) []float64 {
	return k.rows[phase*resampleTaps : (phase+1)*resampleTaps]
}

// at convolves in with the row for one fractional offset.
func (k resampleKernel) at(in []int16, base, phase int) int16 {
	sum := 0.0
	for tap, weight := range k.row(phase) {
		// Hold the edge samples: a synthesized take opens and closes in silence.
		sum += float64(in[min(max(base+tap-resampleCenter, 0), len(in)-1)]) * weight
	}

	return clampSample(sum)
}

// fillSincRow writes the ideal low-pass sinc delayed by frac input samples,
// tapered by a Blackman window whose ~74 dB stopband turns a fold into an
// attenuation. The window is centered on the same fractional instant as the
// sinc: tapering around the integer center instead costs 37 dB of image
// rejection (-46 dB instead of -110 dB at 22.05→16 kHz). Each row is normalized
// on its OWN sum, which varies with frac; one shared normalizer would trade the
// aliasing for a gain changing from output sample to output sample.
func fillSincRow(row []float64, cutoff, frac float64) {
	sum := 0.0
	for tap := range row {
		offset := float64(tap-resampleCenter) - frac
		value := 2 * cutoff
		if offset != 0 {
			value = math.Sin(2*math.Pi*cutoff*offset) / (math.Pi * offset)
		}
		// The taper spans one tap more than the row, so a fractional shift of up
		// to a whole sample cannot push a tap past a zero into a hard edge.
		angle := 2 * math.Pi * offset / float64(len(row)+1)
		value *= 0.42 + 0.5*math.Cos(angle) + 0.08*math.Cos(2*angle)
		row[tap] = value
		sum += value
	}
	for tap := range row {
		row[tap] /= sum
	}
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}

	return a
}

// clampSample rounds to the nearest PCM code instead of truncating, which
// would bias every sample toward zero by up to one LSB.
func clampSample(value float64) int16 {
	switch rounded := math.Round(value); {
	case rounded >= math.MaxInt16:
		return math.MaxInt16
	case rounded <= math.MinInt16:
		return math.MinInt16
	default:
		return int16(rounded)
	}
}

//nolint:gosec // The conversion preserves the exact signed 16-bit PCM bit pattern.
func bytesToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}

	return out
}

//nolint:gosec // The conversion preserves the exact signed 16-bit PCM bit pattern.
func int16ToBytes(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}

	return b
}

// encodeWAV wraps mono 16-bit PCM in a WAV container for the whisper server.
//
//nolint:gosec // Callers bound PCM and sample-rate values to the WAV field widths.
func encodeWAV(pcm []int16, rate int) []byte {
	size := len(pcm) * 2
	wav := make([]byte, 44+size)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+size))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(wav[22:24], 1) // mono
	binary.LittleEndian.PutUint32(wav[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(wav[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(wav[32:34], 2)  // block align
	binary.LittleEndian.PutUint16(wav[34:36], 16) // bits
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(size))
	// Straight into the payload region: one allocation on the STT path.
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(wav[44+i*2:], uint16(v))
	}

	return wav
}
