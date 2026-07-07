package ffmpeg_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

func TestStartMuxEncodesTransportStream(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))

	mux, err := sup.StartMux(context.Background())
	if err != nil {
		t.Fatalf("StartMux returned error: %v", err)
	}

	// Feed a second of tone, then close stdin to drain the encoder.
	feedErr := make(chan error, 1)

	go func() {
		_, wErr := mux.In.Write(tonePCM())
		feedErr <- errors.Join(wErr, mux.In.Close())
	}()

	out, readErr := io.ReadAll(mux.Out)
	if readErr != nil {
		t.Fatalf("reading transport stream: %v", readErr)
	}

	if err := <-feedErr; err != nil {
		t.Fatalf("feeding the mux: %v", err)
	}

	if closeErr := mux.Close(); closeErr != nil {
		t.Fatalf("Close returned error: %v", closeErr)
	}

	if len(out) < 512 {
		t.Fatalf("transport stream is %d bytes, want a real TS", len(out))
	}

	// MPEG-TS packets start with the 0x47 sync byte.
	if out[0] != 0x47 {
		t.Fatalf("first byte = %#x, want the MPEG-TS sync byte 0x47", out[0])
	}
}

func TestStartSinkAcceptsPCM(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))

	// Encode to a null muxer: exercises StartSink without a network target.
	sink, err := sup.StartSink(context.Background(), []string{"-c:a", "aac", "-f", "null", "-"})
	if err != nil {
		t.Fatalf("StartSink returned error: %v", err)
	}

	if _, writeErr := sink.Write(tonePCM()); writeErr != nil {
		t.Fatalf("feeding the sink: %v", writeErr)
	}

	// Give ffmpeg a moment to consume, then stop.
	time.Sleep(200 * time.Millisecond)

	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("Close returned error: %v", closeErr)
	}
}
