//go:build !windows

package ffmpeg

import "os"

func removeReplacedImage(path string) error {
	return os.Remove(path)
}
