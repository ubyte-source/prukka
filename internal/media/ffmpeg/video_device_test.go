package ffmpeg

import (
	"context"
	"errors"
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
	first <- firstErr
	second <- secondErr

	err := <-combineVideoProcesses(ctx, cancel, first, second)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("combined error = %v, want both process failures", err)
	}
	if ctx.Err() == nil {
		t.Fatal("peer context was not canceled")
	}
}
