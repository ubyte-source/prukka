// Package hostos names the operating systems this codebase branches on and
// answers whether a directory entry is a file the host will execute.
package hostos

import (
	"io/fs"
	"runtime"
)

// The runtime.GOOS values the platform dispatch compares against.
const (
	Darwin  = "darwin"
	Linux   = "linux"
	Windows = "windows"
)

// Executable reports whether info describes a regular file this host runs.
// Windows encodes no execute bit, so there being a regular file is the whole
// answer.
func Executable(info fs.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}

	return runtime.GOOS == Windows || info.Mode().Perm()&0o111 != 0
}
