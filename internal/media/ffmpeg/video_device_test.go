package ffmpeg

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestNativeVideoTargetIsExact(t *testing.T) {
	t.Parallel()

	if !IsNativeVideoTarget(NativeVideoTarget) {
		t.Fatal("native target was not recognized")
	}
	if IsNativeVideoTarget(NativeVideoTarget + "/extra") {
		t.Fatal("non-canonical native target was recognized")
	}
}

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
	// Seed and close like waitCommand does: one send, then close, so the
	// combiner's drain of both children observes nil after the value.
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

// TestCombineVideoProcessesJoinsChildrenOnCancel: even when cancellation
// is the trigger, the combined channel is the job's join point — it must
// not report until both children have, and their results must survive in
// the joined error.
func TestCombineVideoProcessesJoinsChildrenOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	first := make(chan error, 1)
	second := make(chan error, 1)
	firstErr := errors.New("controller stopped")
	secondErr := errors.New("encoder stopped")

	// The children report only once the combiner cancels them, the way real
	// feeders die after cancellation propagates through exec's watchdog.
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
