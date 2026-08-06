package core

import (
	"context"
	"io"
	"time"
)

// SourceSpec identifies a media source to ingest.
type SourceSpec struct {
	URL      string        // rtmp://, srt://, file:// or device://
	VideoDir string        // passthrough HLS video rendition; empty disables the tap
	Delay    time.Duration // session delay D, shifting video onto the output clock
}

// Frames delivers labeled PCM from an opened source. Close must be idempotent
// and safe to call concurrently with an in-flight Next, which it interrupts.
type Frames interface {
	// Next blocks for the next frame; it returns io.EOF once the source ends.
	Next(ctx context.Context) (PCM, error)
	io.Closer
}

// Ingress opens a media source and turns it into PCM frames.
type Ingress interface {
	Open(ctx context.Context, src SourceSpec) (Frames, error)
}
