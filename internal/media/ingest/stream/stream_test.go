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
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// newFrames wraps a raw PCM pipe with the default chunk quantum.
func newFrames(pcm io.ReadCloser) *frames {
	return newFramesWithSamples(pcm, pipeline.SamplesInQuantum(DefaultPCMQuantum))
}

// pipePCM is an io.ReadCloser over prebuilt bytes that records its close.
type pipePCM struct {
	fail     error
	closeErr error
	data     []byte
	pos      int
	closed   bool
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

	return p.closeErr
}

type blockingPCM struct {
	readStarted chan struct{}
	released    chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	closes      atomic.Int32
}

func newBlockingPCM() *blockingPCM {
	return &blockingPCM{readStarted: make(chan struct{}), released: make(chan struct{})}
}

func (p *blockingPCM) Read([]byte) (int, error) {
	p.startOnce.Do(func() { close(p.readStarted) })
	<-p.released

	return 0, io.ErrClosedPipe
}

func (p *blockingPCM) Close() error {
	p.closes.Add(1)
	p.releaseOnce.Do(func() { close(p.released) })

	return nil
}

// partialErrorPCM returns data and a terminal error in the same Read call.
type partialErrorPCM struct {
	err    error
	data   []byte
	reads  int
	closes int
}

func (p *partialErrorPCM) Read(b []byte) (int, error) {
	p.reads++
	if p.reads > 1 {
		return 0, io.EOF
	}

	return copy(b, p.data), p.err
}

func (p *partialErrorPCM) Close() error {
	p.closes++

	return nil
}

func requirePartialSourceState(t *testing.T, src *partialErrorPCM, closes int) {
	t.Helper()

	if src.reads != 1 {
		t.Fatalf("source reads = %d, want 1", src.reads)
	}
	if src.closes != closes {
		t.Fatalf("source closes = %d, want %d", src.closes, closes)
	}
}

func requireTerminalError(t *testing.T, f *frames, want error) {
	t.Helper()

	next, err := f.Next(t.Context())
	if !errors.Is(err, want) {
		t.Fatalf("terminal Next error = %v, want %v", err, want)
	}
	if len(next.Data) != 0 {
		t.Fatalf("terminal Next data = %v, want none", next.Data)
	}
}

// ramp builds n little-endian int16 samples as a counter.
func ramp(n int) []byte {
	b := make([]byte, n*2)
	for i := range n {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(i)))
	}

	return b
}

func TestPCMQuantumDefaultsToOneHundredMilliseconds(t *testing.T) {
	t.Parallel()

	ingress := New(nil)
	want := core.SampleRate / 10
	if ingress.quantumSamples != want {
		t.Fatalf("default quantum = %d samples, want %d", ingress.quantumSamples, want)
	}
}

func TestPCMQuantumControlsDeliverySizeAndPTS(t *testing.T) {
	t.Parallel()

	const quantum = 20 * time.Millisecond
	ingress := New(nil, WithPCMQuantum(quantum))
	src := &pipePCM{data: ramp(ingress.quantumSamples * 2)}
	f := newFramesWithSamples(src, ingress.quantumSamples)

	first := mustNext(t, f)
	if len(first.Data) != core.SampleRate/50 {
		t.Fatalf("first delivery = %d samples, want 20 ms", len(first.Data))
	}
	second := mustNext(t, f)
	if second.PTS != quantum {
		t.Fatalf("second PTS = %v, want %v", second.PTS, quantum)
	}
}

func TestPCMQuantumRejectsInvalidDurations(t *testing.T) {
	t.Parallel()

	for name, quantum := range map[string]time.Duration{
		"negative":    -time.Millisecond,
		"not aligned": pipeline.SamplePeriod + time.Nanosecond,
		"zero":        0,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatalf("WithPCMQuantum(%v) did not panic", quantum)
				}
			}()

			_ = WithPCMQuantum(quantum)
		})
	}
}

func TestDeviceCaptureBufferConfiguresIngress(t *testing.T) {
	t.Parallel()

	ingress := New(nil, WithDeviceCaptureBuffer(20*time.Millisecond))
	if ingress.deviceBuffer != 20*time.Millisecond {
		t.Fatalf("device capture buffer = %s, want 20ms", ingress.deviceBuffer)
	}
}

func TestNextDecodesChunksWithContinuousPTS(t *testing.T) {
	t.Parallel()

	// Two full 100 ms chunks of a ramp.
	chunk := core.SampleRate / 10
	src := &pipePCM{data: ramp(chunk * 2)}
	f := newFrames(src)

	first := mustNext(t, f)
	if len(first.Data) != chunk || first.Rate != core.SampleRate || first.Ch != 1 {
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

	src := &pipePCM{data: ramp(core.SampleRate)}
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

func TestNextCancellationInterruptsBlockedRead(t *testing.T) {
	src := newBlockingPCM()
	f := newFrames(src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := f.Next(ctx)
		done <- err
	}()

	select {
	case <-src.readStarted:
	case <-time.After(time.Second):
		if err := f.Close(); err != nil {
			t.Errorf("timeout cleanup Close error = %v", err)
		}
		t.Fatal("Next did not enter the blocking read")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled blocked Next error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		if err := f.Close(); err != nil {
			t.Errorf("timeout cleanup Close error = %v", err)
		}
		t.Fatal("cancellation did not interrupt the blocking read")
	}

	if err := f.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
	if got := src.closes.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
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

func TestNextDefersPartialPipeErrorWithoutRepeatingData(t *testing.T) {
	t.Parallel()

	want := errors.New("partial read failed")
	src := &partialErrorPCM{data: ramp(3), err: want}
	f := newFrames(src)

	got, err := f.Next(t.Context())
	if err != nil {
		t.Fatalf("partial Next error = %v, want delivered data", err)
	}
	if !slices.Equal(got.Data, []int16{0, 1, 2}) {
		t.Fatalf("partial Next data = %v, want [0 1 2]", got.Data)
	}
	requirePartialSourceState(t, src, 0)

	requireTerminalError(t, f, want)
	requirePartialSourceState(t, src, 1)

	requireTerminalError(t, f, want)
	requirePartialSourceState(t, src, 1)
}

func TestNextDefersPartialCleanEnd(t *testing.T) {
	t.Parallel()

	for name, readErr := range map[string]error{
		"EOF":            io.EOF,
		"unexpected EOF": io.ErrUnexpectedEOF,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := &partialErrorPCM{data: ramp(2), err: readErr}
			f := newFrames(src)

			if got := mustNext(t, f); len(got.Data) != 2 {
				t.Fatalf("partial data length = %d, want 2", len(got.Data))
			}
			if _, err := f.Next(t.Context()); !errors.Is(err, io.EOF) {
				t.Fatalf("deferred clean end = %v, want io.EOF", err)
			}
			if _, err := f.Next(t.Context()); !errors.Is(err, io.EOF) {
				t.Fatalf("repeated clean end = %v, want io.EOF", err)
			}
			requirePartialSourceState(t, src, 1)
		})
	}
}

func TestNextRejectsTruncatedPCMSampleAtEnd(t *testing.T) {
	t.Parallel()

	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			t.Parallel()

			src := &partialErrorPCM{data: make([]byte, size), err: io.EOF}
			f := newFrames(src)
			if size > 1 {
				got, err := f.Next(t.Context())
				if err != nil || len(got.Data) != 1 {
					t.Fatalf("complete prefix = (%v, %v), want one sample", got.Data, err)
				}
			}

			requireTerminalError(t, f, errTruncatedPCM)
			requireTerminalError(t, f, errTruncatedPCM)
			requirePartialSourceState(t, src, 1)
		})
	}
}

func TestNextPreservesDeferredPipeErrorOnCancellation(t *testing.T) {
	t.Parallel()

	want := errors.New("partial read failed")
	src := &partialErrorPCM{data: ramp(1), err: want}
	f := newFrames(src)

	if got := mustNext(t, f); len(got.Data) != 1 {
		t.Fatalf("partial data length = %d, want 1", len(got.Data))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := f.Next(ctx); !errors.Is(err, want) || !errors.Is(err, context.Canceled) {
		t.Fatalf("deferred canceled Next = %v, want joined source and cancellation errors", err)
	}
	requirePartialSourceState(t, src, 1)
}

func TestNextReportsAProcessFailureAtEOF(t *testing.T) {
	t.Parallel()

	want := errors.New("ffmpeg exited 1")
	src := &pipePCM{closeErr: want}
	f := newFrames(src)

	if _, err := f.Next(t.Context()); !errors.Is(err, want) || errors.Is(err, io.EOF) {
		t.Fatalf("Next error = %v, want process failure without clean EOF", err)
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
