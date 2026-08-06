package ffmpeg

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestStartVideoDeviceRejectsUnknownTarget(t *testing.T) {
	t.Parallel()

	s := NewSupervisor("", nil)
	if _, err := s.StartVideoDevice(t.Context(), "index.m3u8", "device://video/unknown"); err == nil {
		t.Fatal("unknown native target was accepted")
	}
}

func TestCombineVideoProcessesCancelsPeerAndJoinsFailures(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	second := make(chan error, 1)
	firstErr := errors.New("controller stopped")
	secondErr := errors.New("encoder stopped")
	// Seed and close like waitCommand does: one send, then close.
	first <- firstErr
	close(first)
	second <- secondErr
	close(second)

	err := <-combineVideoProcesses(ctx, cancel, first, second)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("combined error = %v, want both process failures", err)
	}
	if ctx.Err() == nil {
		t.Fatal("peer context was not canceled")
	}
}

func TestCombineVideoProcessesJoinsChildrenOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	first := make(chan error, 1)
	second := make(chan error, 1)
	firstErr := errors.New("controller stopped")
	secondErr := errors.New("encoder stopped")

	// The children report only once the combiner cancels them.
	var once sync.Once
	release := func() {
		once.Do(func() {
			first <- firstErr
			close(first)
			second <- secondErr
			close(second)
		})
	}

	err := <-combineVideoProcesses(ctx, release, first, second)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("combined error = %v, want cancellation joined with both children", err)
	}
}
