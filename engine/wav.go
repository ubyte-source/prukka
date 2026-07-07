package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
		if errors.Is(err, os.ErrNotExist) {
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
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return errors.New("WAV has invalid RIFF/WAVE signature")
	}
	wantSize := int64(binary.LittleEndian.Uint32(header[4:8])) + 8
	if wantSize > info.Size() {
		return errIncompleteWAV
	}
	if wantSize != info.Size() {
		return fmt.Errorf("WAV RIFF size %d does not match file size %d", wantSize, info.Size())
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
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
	if string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return errors.New("WAV has invalid RIFF/WAVE signature")
	}
	wantSize := uint64(binary.LittleEndian.Uint32(raw[4:8])) + 8
	if wantSize > uint64(len(raw)) {
		return errIncompleteWAV
	}
	if wantSize != uint64(len(raw)) {
		return fmt.Errorf("WAV RIFF size %d does not match file size %d", wantSize, len(raw))
	}

	return nil
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

// resampleLinear resamples 16-bit PCM between rates by linear interpolation —
// adequate for speech; a polyphase filter is a later refinement.
func resampleLinear(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return in
	}
	outLen := len(in) * to / from
	out := make([]int16, outLen)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		left := int(pos)
		frac := pos - float64(left)
		if left+1 < len(in) {
			out[i] = int16(float64(in[left])*(1-frac) + float64(in[left+1])*frac)
		} else {
			out[i] = in[len(in)-1]
		}
	}

	return out
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
	data := int16ToBytes(pcm)
	wav := make([]byte, 44+len(data))
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+len(data)))
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
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(data)))
	copy(wav[44:], data)

	return wav
}
