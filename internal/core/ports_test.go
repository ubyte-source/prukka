package core_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

const (
	referenceFrame   = 10 * time.Millisecond
	referenceSamples = int(core.SampleRate * referenceFrame / time.Second) // 160, mono
)

type memoryIngress struct{ ready int }

func (i memoryIngress) Open(_ context.Context, src core.SourceSpec) (core.Frames, error) {
	return &memorySource{closed: make(chan struct{}), delay: src.Delay, ready: i.ready}, nil
}

type memorySource struct {
	closed    chan struct{}
	closeOnce sync.Once
	delay     time.Duration
	ready     int
	sent      int
}

func (s *memorySource) Next(ctx context.Context) (core.PCM, error) {
	if s.sent < s.ready {
		frame := core.PCM{
			Data: make([]int16, referenceSamples),
			Rate: core.SampleRate,
			Ch:   1,
			PTS:  s.delay + time.Duration(s.sent)*referenceFrame,
		}
		s.sent++

		return frame, nil
	}

	// A live source with nothing buffered blocks here — the state Close's
	// contract is about.
	select {
	case <-s.closed:
		return core.PCM{}, io.EOF
	case <-ctx.Done():
		return core.PCM{}, ctx.Err()
	}
}

func (s *memorySource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })

	return nil
}

var (
	_ core.Ingress = memoryIngress{}
	_ core.Frames  = (*memorySource)(nil)
)

func TestIngressLabelsFramesWithTheCanonicalFormat(t *testing.T) {
	t.Parallel()

	frames, err := memoryIngress{ready: 3}.Open(t.Context(), core.SourceSpec{
		URL:   "file:///tmp/input.wav",
		Delay: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if closeErr := frames.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	}()

	for i := range 3 {
		frame, nextErr := frames.Next(t.Context())
		if nextErr != nil {
			t.Fatalf("frame %d: %v", i, nextErr)
		}
		want := 2*time.Second + time.Duration(i)*referenceFrame
		if frame.Rate != core.SampleRate || frame.Ch != 1 || frame.PTS != want {
			t.Fatalf("frame %d = %dHz/%dch at %v, want %dHz/1ch at %v",
				i, frame.Rate, frame.Ch, frame.PTS, core.SampleRate, want)
		}
	}
}

func TestFramesCloseInterruptsNextAndRepeats(t *testing.T) {
	t.Parallel()

	frames, err := memoryIngress{}.Open(t.Context(), core.SourceSpec{URL: "device://audio/microphone"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	blocked := make(chan error, 1)
	go func() {
		_, nextErr := frames.Next(t.Context())
		blocked <- nextErr
	}()

	if closeErr := frames.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if closeErr := frames.Close(); closeErr != nil {
		t.Fatalf("second close: %v", closeErr)
	}

	if nextErr := <-blocked; !errors.Is(nextErr, io.EOF) {
		t.Fatalf("interrupted Next = %v, want io.EOF", nextErr)
	}
}
