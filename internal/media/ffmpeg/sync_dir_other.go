//go:build !darwin && !linux && !windows

package ffmpeg

func syncDir(string) error {
	return nil
}
