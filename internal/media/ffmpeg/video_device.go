package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/redact"
)

// NativeVideoAvailable reports whether this installation can launch its
// platform feeder; Linux webcams use their /dev/video path instead.
func NativeVideoAvailable(ctx context.Context) bool {
	if runtime.GOOS != hostos.Darwin && runtime.GOOS != hostos.Windows {
		return false
	}

	return nativeVideoAvailable(ctx)
}

// StartVideoDevice launches the installed platform feeder over one HLS video
// rendition; the returned channel reports its single terminal result.
func (s *Supervisor) StartVideoDevice(ctx context.Context, playlist, target string) (<-chan error, error) {
	if !deviceurl.IsNativeVideo(target) {
		return nil, fmt.Errorf("native video target %s is unsupported", redact.URL(target))
	}

	return s.startNativeVideoDevice(ctx, playlist)
}

// combineVideoProcesses cancels the peer once either child or the context ends,
// then reports the joined result of BOTH children: the channel must not fire
// while a child still holds the virtual camera.
func combineVideoProcesses(
	ctx context.Context, cancel context.CancelFunc, first, second <-chan error,
) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)

		var trigger error
		select {
		case trigger = <-first:
		case trigger = <-second:
		case <-ctx.Done():
			trigger = ctx.Err()
		}

		cancel()
		done <- errors.Join(trigger, <-first, <-second)
	}()

	return done
}
