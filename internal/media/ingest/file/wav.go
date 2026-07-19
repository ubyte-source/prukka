package file

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

const (
	maxRIFFFileBytes = int64(1<<32 + 7)
	maxRIFFChunks    = 4096
)

type wavSpec struct {
	dataOffset int64
	dataBytes  int64
}

type wavChunk struct {
	id         string
	bodyOffset int64
	bodyBytes  int64
	nextOffset int64
}

type wavInspection struct {
	r          io.ReaderAt
	end        int64
	offset     int64
	chunks     int
	spec       wavSpec
	formatSeen bool
	dataSeen   bool
}

// inspectWAV validates a 16 kHz mono 16-bit RIFF/WAVE source without loading
// its PCM payload.
func inspectWAV(r io.ReaderAt, fileSize int64) (wavSpec, error) {
	riffEnd, err := inspectRIFFHeader(r, fileSize)
	if err != nil {
		return wavSpec{}, err
	}

	inspection := wavInspection{r: r, end: riffEnd, offset: 12}
	for inspection.offset < inspection.end {
		if err := inspection.consumeNext(); err != nil {
			return wavSpec{}, err
		}
	}

	if !inspection.formatSeen || !inspection.dataSeen {
		return wavSpec{}, errors.New("missing fmt or data chunk")
	}

	return inspection.spec, nil
}

func inspectRIFFHeader(r io.ReaderAt, fileSize int64) (int64, error) {
	if fileSize < 12 {
		return 0, errors.New("not a RIFF/WAVE file")
	}
	if fileSize > maxRIFFFileBytes {
		return 0, fmt.Errorf("WAV file exceeds RIFF limit of %d bytes", maxRIFFFileBytes)
	}

	header := make([]byte, 12)
	if err := readAt(r, header, 0); err != nil {
		return 0, fmt.Errorf("read WAV header: %w", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, errors.New("not a RIFF/WAVE file")
	}

	riffEnd := int64(binary.LittleEndian.Uint32(header[4:8])) + 8
	if riffEnd < 12 {
		return 0, errors.New("invalid RIFF size")
	}
	if riffEnd > fileSize {
		return 0, errors.New("truncated RIFF payload")
	}

	return riffEnd, nil
}

func (s *wavInspection) consumeNext() error {
	if s.chunks >= maxRIFFChunks {
		return fmt.Errorf("WAV contains more than %d chunks", maxRIFFChunks)
	}

	chunk, err := inspectChunk(s.r, s.offset, s.end)
	if err != nil {
		return err
	}

	s.chunks++
	s.offset = chunk.nextOffset

	switch chunk.id {
	case "fmt ":
		if s.formatSeen {
			return errors.New("duplicate fmt chunk")
		}
		if err := inspectFormat(s.r, chunk.bodyOffset, chunk.bodyBytes); err != nil {
			return err
		}
		s.formatSeen = true
	case "data":
		if err := s.consumeData(chunk); err != nil {
			return err
		}
	}

	return nil
}

func inspectChunk(r io.ReaderAt, offset, riffEnd int64) (wavChunk, error) {
	if riffEnd-offset < 8 {
		return wavChunk{}, errors.New("truncated WAV chunk header")
	}

	header := make([]byte, 8)
	if err := readAt(r, header, offset); err != nil {
		return wavChunk{}, fmt.Errorf("read WAV chunk header: %w", err)
	}

	id := string(header[0:4])
	bodyBytes := int64(binary.LittleEndian.Uint32(header[4:8]))
	bodyOffset := offset + 8
	bodyEnd := bodyOffset + bodyBytes
	paddedEnd := bodyEnd + bodyBytes%2
	if bodyEnd < bodyOffset || paddedEnd > riffEnd {
		return wavChunk{}, fmt.Errorf("truncated %q chunk", id)
	}

	return wavChunk{id: id, bodyOffset: bodyOffset, bodyBytes: bodyBytes, nextOffset: paddedEnd}, nil
}

func (s *wavInspection) consumeData(chunk wavChunk) error {
	if s.dataSeen {
		return errors.New("duplicate data chunk")
	}
	if chunk.bodyBytes == 0 {
		return errors.New("empty data chunk")
	}
	if chunk.bodyBytes%2 != 0 {
		return errors.New("unaligned 16-bit PCM data")
	}

	s.spec = wavSpec{dataOffset: chunk.bodyOffset, dataBytes: chunk.bodyBytes}
	s.dataSeen = true

	return nil
}

func inspectFormat(r io.ReaderAt, offset, size int64) error {
	if size < 16 {
		return errors.New("short fmt chunk")
	}

	format := make([]byte, 16)
	if err := readAt(r, format, offset); err != nil {
		return fmt.Errorf("read fmt chunk: %w", err)
	}

	encoding := binary.LittleEndian.Uint16(format[0:2])
	channels := binary.LittleEndian.Uint16(format[2:4])
	rate := binary.LittleEndian.Uint32(format[4:8])
	byteRate := binary.LittleEndian.Uint32(format[8:12])
	blockAlign := binary.LittleEndian.Uint16(format[12:14])
	bits := binary.LittleEndian.Uint16(format[14:16])

	if encoding != 1 || bits != 16 || channels != 1 || rate != pipeline.SampleRate {
		return fmt.Errorf(
			"need 16 kHz mono 16-bit PCM, got format=%d %d Hz × %d ch %d bit — "+
				"resample with: ffmpeg -i in -ar 16000 -ac 1 out.wav", encoding, rate, channels, bits)
	}
	if byteRate != pipeline.SampleRate*2 || blockAlign != 2 {
		return fmt.Errorf("invalid PCM layout: byte rate=%d block align=%d", byteRate, blockAlign)
	}

	return nil
}

func readAt(r io.ReaderAt, dst []byte, offset int64) error {
	n, err := r.ReadAt(dst, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n != len(dst) {
		return io.ErrUnexpectedEOF
	}

	return nil
}
