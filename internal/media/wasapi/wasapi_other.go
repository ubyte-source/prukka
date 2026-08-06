//go:build !windows

package wasapi

import (
	"errors"
	"io"
)

// open is Windows-only; other platforms route device targets through
// ffmpeg's device muxers (media/ffmpeg).
func open(string, openConfig) (io.WriteCloser, error) {
	return nil, errors.New("wasapi: Windows only")
}
