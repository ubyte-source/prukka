package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// NativeVideoTarget is the stable URL of Prukka's platform webcam feeder.
const NativeVideoTarget = "device://video/prukka"

// IsNativeVideoTarget reports whether target selects the platform feeder.
func IsNativeVideoTarget(target string) bool {
	return target == NativeVideoTarget
}

// NativeVideoAvailable reports whether this installation can launch its
// platform feeder. Linux webcams use their /dev/video path instead.
func NativeVideoAvailable(ctx context.Context) bool {
	if runtime.GOOS != osDarwin && runtime.GOOS != osWindows {
		return false
	}

	return nativeVideoAvailable(ctx)
}

// StartVideoDevice launches the installed platform feeder over one HLS video
// rendition. The returned channel reports its single terminal result.
func (s *Supervisor) StartVideoDevice(ctx context.Context, playlist, target string) (<-chan error, error) {
	if !IsNativeVideoTarget(target) {
		return nil, fmt.Errorf("native video target %q is unsupported", target)
	}

	return s.startNativeVideoDevice(ctx, playlist)
}

func combineVideoProcesses(
	ctx context.Context, cancel context.CancelFunc, first, second <-chan error,
) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)
		defer cancel()

		select {
		case err := <-first:
			cancel()
			done <- errors.Join(err, <-second)
		case err := <-second:
			cancel()
			done <- errors.Join(err, <-first)
		case <-ctx.Done():
			cancel()
			done <- ctx.Err()
		}
	}()

	return done
}
