//go:build !windows

package wasapi

import (
	"errors"
	"io"
)

// Open is Windows-only; other platforms route device targets through
// ffmpeg's device muxers (media/ffmpeg).
func Open(string, ...OpenOption) (io.WriteCloser, error) {
	return nil, errors.New("wasapi: Windows only")
}
