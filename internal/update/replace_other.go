//go:build !windows

package update

import "os"

func removeReplacedImage(path string) error {
	return os.Remove(path)
}
