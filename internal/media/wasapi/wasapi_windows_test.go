//go:build windows

package wasapi

import (
	"errors"
	"io"
	"testing"
)

func TestAwaitOpenPublishesReadyFormatDespiteQueuedRuntimeError(t *testing.T) {
	t.Parallel()

	w := &writer{
		errs: make(chan error, 1),
		done: make(chan struct{}),
	}
	w.errs <- errors.New("runtime failure must not race initialization")
	ready := make(chan openResult, 1)
	ready <- openResult{rate: 48000, chans: 2}

	opened, err := awaitOpen(w, ready)
	if err != nil {
		t.Fatalf("awaitOpen error = %v, want ready writer", err)
	}
	if opened != w {
		t.Fatalf("awaitOpen writer = %v, want %p", opened, w)
	}
	if w.rate != 48000 || w.chans != 2 {
		t.Fatalf("ready format = %d Hz x %d, want 48000 Hz x 2", w.rate, w.chans)
	}
}

func TestAwaitOpenReportsInitializationFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("start failed")
	done := make(chan struct{})
	close(done)
	w := &writer{done: done}
	ready := make(chan openResult, 1)
	ready <- openResult{err: want}

	opened, err := awaitOpen(w, ready)
	if opened != nil || !errors.Is(err, want) {
		t.Fatalf("awaitOpen = (%v, %v), want (nil, initialization error)", opened, err)
	}
}

func TestWriterReusesConvertedFrameBuffer(t *testing.T) {
	t.Parallel()

	storage := make([]float32, 1)
	w := &writer{
		frames: make(chan []float32, 1),
		free:   make(chan []float32, 1),
		errs:   make(chan error),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
		rate:   sourceRate,
		chans:  1,
	}
	w.free <- storage[:0]

	n, err := w.Write([]byte{1, 0})
	if err != nil || n != 2 {
		t.Fatalf("Write = (%d, %v), want (2, nil)", n, err)
	}
	frames := <-w.frames
	if &frames[0] != &storage[0] {
		t.Fatal("Write replaced the recycled conversion buffer")
	}
}

func TestCloseCancelsSubmitWithFullPadding(t *testing.T) {
	t.Parallel()

	w := &writer{
		errs: make(chan error, 1),
		done: make(chan struct{}),
		stop: make(chan struct{}),
	}
	paddingRead := make(chan struct{})
	submitResult := make(chan error, 1)

	go func() {
		defer close(w.done)

		var announced bool
		readPadding := func() (uint32, error) {
			if !announced {
				announced = true
				close(paddingRead)
			}

			return 8, nil
		}
		submitResult <- submitWithPadding(w.stop, nil, com{}, 8, 1, []float32{1}, readPadding)
	}()

	<-paddingRead
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned an error for an owned stop: %v", err)
	}
	if err := <-submitResult; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("submit error = %v, want io.ErrClosedPipe", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close returned an error: %v", err)
	}
}

func TestWriteAfterComThreadFailureReturnsTheRealError(t *testing.T) {
	t.Parallel()

	want := errors.New("wasapi: GetBuffer: 0x88890004")
	for range 8 {
		done := make(chan struct{})
		close(done)
		w := &writer{
			frames: make(chan []float32, 1),
			free:   make(chan []float32, 1),
			errs:   make(chan error, 1),
			done:   done,
			stop:   make(chan struct{}),
			rate:   sourceRate,
			chans:  1,
		}
		w.errs <- want

		if n, err := w.Write([]byte{1, 0}); n != 0 || !errors.Is(err, want) {
			t.Fatalf("Write after COM failure = (%d, %v), want (0, %v)", n, err, want)
		}
		if n, err := w.Write([]byte{1, 0}); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("second Write = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
		}
	}
}

func TestWriteAfterCloseReturnsClosedPipe(t *testing.T) {
	t.Parallel()

	w := &writer{
		frames: make(chan []float32),
		free:   make(chan []float32, 1),
		errs:   make(chan error, 1),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
		rate:   sourceRate,
		chans:  1,
	}
	go func() {
		<-w.stop
		close(w.done)
	}()

	if err := w.Close(); err != nil {
		t.Fatalf("Close returned an error: %v", err)
	}
	if n, err := w.Write([]byte{1, 0}); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after Close = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
}
