package stream

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// pipePCM is an io.ReadCloser over prebuilt bytes that records its close.
type pipePCM struct {
	fail   error
	data   []byte
	pos    int
	closed bool
}

func (p *pipePCM) Read(b []byte) (int, error) {
	if p.fail != nil {
		return 0, p.fail
	}

	if p.pos >= len(p.data) {
		return 0, io.EOF
	}

	n := copy(b, p.data[p.pos:])
	p.pos += n

	return n, nil
}

func (p *pipePCM) Close() error {
	p.closed = true

	return nil
}

// ramp builds n little-endian int16 samples as a counter.
func ramp(n int) []byte {
	b := make([]byte, n*2)
	for i := range n {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(i)))
	}

	return b
}

func TestNextDecodesChunksWithContinuousPTS(t *testing.T) {
	t.Parallel()

	// Two full 100 ms chunks of a ramp.
	chunk := pipeline.SampleRate / 10
	src := &pipePCM{data: ramp(chunk * 2)}
	f := newFrames(src)

	first := mustNext(t, f)
	if len(first.Data) != chunk || first.Rate != pipeline.SampleRate || first.Ch != 1 {
		t.Fatalf("first chunk = %d samples @%d×%d, want %d @16000×1", len(first.Data), first.Rate, first.Ch, chunk)
	}

	if first.PTS != 0 || first.Data[0] != 0 || first.Data[1] != 1 {
		t.Fatalf("first chunk PTS/decode wrong: PTS=%v data[:2]=%v", first.PTS, first.Data[:2])
	}

	if second := mustNext(t, f); second.PTS != 100*time.Millisecond {
		t.Fatalf("second PTS = %v, want 100ms after the first chunk", second.PTS)
	}

	// The stream ends: EOF, and the pipe is closed.
	if _, eofErr := f.Next(context.Background()); !errors.Is(eofErr, io.EOF) {
		t.Fatalf("end-of-stream error = %v, want io.EOF", eofErr)
	}

	if !src.closed {
		t.Fatal("pipe not closed at end of stream")
	}
}

// mustNext reads one chunk, failing the test on error.
func mustNext(t *testing.T, f *frames) core.PCM {
	t.Helper()

	out, err := f.Next(context.Background())
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}

	return out
}

func TestNextClosesPipeOnCancellation(t *testing.T) {
	t.Parallel()

	src := &pipePCM{data: ramp(pipeline.SampleRate)}
	f := newFrames(src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Next error = %v, want context.Canceled", err)
	}

	if !src.closed {
		t.Fatal("pipe not closed on cancellation")
	}
}

func TestNextReportsPipeErrors(t *testing.T) {
	t.Parallel()

	src := &pipePCM{fail: errors.New("broken pipe")}
	f := newFrames(src)

	if _, err := f.Next(context.Background()); err == nil {
		t.Fatal("Next succeeded over a broken pipe, want error")
	}

	if !src.closed {
		t.Fatal("pipe not closed after a read error")
	}
}

// TestOpenStartsAndStopsWithTheContext (live): cancel must end the stream,
// not hang a reader on a dead pipe.
func TestOpenStartsAndStopsWithTheContext(t *testing.T) {
	t.Parallel()

	bin := os.Getenv("PRUKKA_TEST_FFMPEG")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("ffmpeg"); err != nil {
			t.Skip("no ffmpeg available; set PRUKKA_TEST_FFMPEG or install one")
		}
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %T is not TCP", listener.Addr())
	}

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("release port: %v", closeErr)
	}

	ctx, cancel := context.WithCancel(t.Context())

	sup := ffmpeg.NewSupervisor(bin, slog.New(slog.DiscardHandler))

	frames, err := New(sup).Open(ctx, core.SourceSpec{
		URL: fmt.Sprintf("rtmp://127.0.0.1:%d/live/in", addr.Port),
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	// No encoder ever connects: cancel must end the blocked reader.
	cancel()

	readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readCancel()

	if _, nextErr := frames.Next(readCtx); nextErr == nil {
		t.Fatal("Next returned audio from a canceled source")
	}

	if readCtx.Err() != nil {
		t.Fatal("Next hung past cancellation instead of ending with the process")
	}
}
