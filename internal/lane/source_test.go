package lane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	fileingress "github.com/ubyte-source/prukka/internal/media/ingest/file"
)

type blockingIngress struct {
	frames  core.Frames
	started chan struct{}
	release chan struct{}
}

func (i blockingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	close(i.started)
	<-i.release

	return i.frames, nil
}

type closeTrackedFrames struct {
	closeErr error
	mu       sync.Mutex
	closes   int
	nexts    int
}

func (f *closeTrackedFrames) Next(context.Context) (core.PCM, error) {
	f.mu.Lock()
	f.nexts++
	f.mu.Unlock()

	return core.PCM{}, io.EOF
}

func (f *closeTrackedFrames) Close() error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()

	return f.closeErr
}

func (f *closeTrackedFrames) counts() (closes, nexts int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closes, f.nexts
}

func TestObservedFramesSignalsOnlyAfterMediaFlows(t *testing.T) {
	t.Parallel()

	signals := 0
	wantCloseErr := errors.New("close source")
	source := &scriptedFrames{results: []frameResult{
		{err: errors.New("not ready")},
		{frame: core.PCM{Data: []int16{1}, Rate: 16_000, Ch: 1}},
		{err: io.EOF},
	}, closeErr: wantCloseErr}
	frames := &observedFrames{
		Frames:  source,
		running: func() { signals++ },
	}

	if _, err := frames.Next(t.Context()); err == nil || signals != 0 {
		t.Fatalf("failed read = %v, signals = %d; want error and no running signal", err, signals)
	}
	if _, err := frames.Next(t.Context()); err != nil || signals != 1 {
		t.Fatalf("media read = %v, signals = %d; want success and one signal", err, signals)
	}
	if _, err := frames.Next(t.Context()); !errors.Is(err, io.EOF) || signals != 1 {
		t.Fatalf("EOF read = %v, signals = %d; want EOF and one signal", err, signals)
	}
	if err := frames.Close(); !errors.Is(err, wantCloseErr) || source.closed != 1 {
		t.Fatalf("Close = %v, wrapped closes = %d; want wrapped error and one", err, source.closed)
	}
}

func TestLazyFramesCancellationClosesSourceReturnedByInFlightOpen(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("eventual close")
	source := &closeTrackedFrames{closeErr: closeErr}
	ingress := blockingIngress{
		frames:  source,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	frames := newLazyFrames(ingress, core.SourceSpec{URL: "device://audio/microphone"})
	ctx, cancel := context.WithCancel(context.Background())
	nextDone := make(chan error, 1)
	go func() {
		_, err := frames.Next(ctx)
		nextDone <- err
	}()
	<-ingress.started
	cancel()

	closeDone := make(chan error, 1)
	go func() { closeDone <- frames.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close during Open = %v, want no source result yet", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on an in-flight ingress Open")
	}

	close(ingress.release)
	if err := <-nextDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after cancellation = %v, want context.Canceled", err)
	}
	if err := frames.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close after eventual source = %v, want %v", err, closeErr)
	}
	if closes, nexts := source.counts(); closes != 1 || nexts != 0 {
		t.Fatalf("eventual source lifecycle = %d closes/%d reads, want 1/0", closes, nexts)
	}
}

// TestLazyFramesConcurrentClosesShareOneSourceClose: every closer, including
// the last to arrive, runs the same underlying Close and reads its one result.
func TestLazyFramesConcurrentClosesShareOneSourceClose(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("published close")
	source := &closeTrackedFrames{closeErr: closeErr}
	frames := newLazyFrames(recordingIngress{
		frames: source,
		opened: make(chan struct{}),
	}, core.SourceSpec{URL: "device://audio/microphone"})

	if _, err := frames.Next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("first read = %v, want the source's EOF", err)
	}

	results := make(chan error, 4)
	var closers sync.WaitGroup
	for range cap(results) {
		closers.Go(func() {
			results <- frames.Close()
		})
	}
	closers.Wait()
	close(results)

	for err := range results {
		if !errors.Is(err, closeErr) {
			t.Fatalf("concurrent Close = %v, want the cached %v", err, closeErr)
		}
	}
	if closes, _ := source.counts(); closes != 1 {
		t.Fatalf("underlying closes = %d, want exactly one", closes)
	}
}

func TestIngressForKeepsLoopingWAVOnTheNativeReader(t *testing.T) {
	t.Parallel()

	ingress, err := ingressFor(
		"file:///tmp/take.WAV?loop=true", session.ProfileCall, slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("ingressFor returned error: %v", err)
	}
	if _, ok := ingress.(fileingress.Ingress); !ok {
		t.Fatalf("ingress type = %T, want native WAV ingress", ingress)
	}
}

// TestIngressForRedactsTheRejectedSource pins that an unusable source is named
// by scheme and host only: its stream key never reaches the session status.
func TestIngressForRedactsTheRejectedSource(t *testing.T) {
	t.Parallel()

	_, err := ingressFor(
		"https://user:hunter2@live.example/app/streamkey?secret=abc",
		session.ProfileBroadcast, slog.New(slog.DiscardHandler),
	)
	if err == nil {
		t.Fatal("ingressFor accepted an unsupported scheme")
	}
	for _, secret := range []string{"user", "hunter2", "streamkey", "abc"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposes %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "https://live.example") {
		t.Fatalf("error lost the safe endpoint label: %v", err)
	}
}
