package ffmpeg

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

// micCaptureBinaryName is the native audio-device helper shipped beside the
// daemon executable.
const micCaptureBinaryName = "prukka-miccapture"

// MicCaptureBinary resolves the native audio-device helper beside the running
// executable, then inside the managed runtime bundle at bundleRoot, or "" when
// absent.
func MicCaptureBinary(bundleRoot string) string {
	if runtime.GOOS != hostos.Darwin {
		return ""
	}

	dirs := make([]string, 0, 2)
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if bundleRoot != "" {
		dirs = append(dirs, bundleRoot)
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, micCaptureBinaryName)
		info, err := os.Stat(path)
		if err == nil && hostos.Executable(info) {
			return path
		}
	}

	return ""
}

// micCaptureCommand builds the native helper invocation for a macOS audio-only
// device source, reporting false so ffmpeg demuxes everything else.
func micCaptureCommand(goos, helper, src, videoDir string) (bin string, args []string, ok bool) {
	if helper == "" || videoDir != "" || goos != hostos.Darwin {
		return "", nil, false
	}

	name, ok := micCaptureName(src)
	if !ok {
		return "", nil, false
	}

	return helper, micCaptureArgs(name), true
}

// micCaptureName returns the AVFoundation device name the helper should open,
// preferring the display-name hint over the positional id.
func micCaptureName(src string) (string, bool) {
	ref, err := deviceurl.Parse(src)
	if err != nil || ref.Kind != deviceurl.Audio {
		return "", false
	}

	// A colon reads as AVFoundation's video:audio separator.
	if ref.Label != "" && !strings.Contains(ref.Label, ":") {
		return ref.Label, true
	}

	return ref.ID, true
}

// micCaptureArgs is the helper's fixed invocation; it streams s16le mono at
// the reference sample rate.
func micCaptureArgs(name string) []string {
	return []string{"--device", name, "--rate", strconv.Itoa(core.SampleRate)}
}
