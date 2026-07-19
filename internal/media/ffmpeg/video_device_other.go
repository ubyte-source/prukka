//go:build !darwin && !windows

package ffmpeg

import (
	"context"
	"errors"
)

func nativeVideoAvailable(context.Context) bool { return false }

func (s *Supervisor) startNativeVideoDevice(context.Context, string) (<-chan error, error) {
	return nil, errors.New("native Prukka webcam output is not integrated on this platform")
}
