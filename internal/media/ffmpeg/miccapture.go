package ffmpeg

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// micCaptureBinaryName is the native microphone-capture helper shipped beside
// the daemon executable inside the macOS app bundle.
const micCaptureBinaryName = "prukka-miccapture"

// MicCaptureBinary resolves the native microphone-capture helper: beside the
// running executable first (an operator-managed install), then inside the
// managed runtime bundle at bundleRoot, or "" when absent (development builds
// and every non-darwin platform). FFmpeg's raw AVFoundation input is delivered
// silent to a process launchd started, because it never asks CoreAudio for
// capture authorization; the helper opens the device through an
// AVCaptureSession that does, so the daemon prefers it for audio-device
// sources whenever it ships.
func MicCaptureBinary(bundleRoot string) string {
	if runtime.GOOS != osDarwin {
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
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}

	return ""
}

// micCaptureCommand builds the native helper invocation for a macOS
// audio-device source. It reports false — deferring to ffmpeg — when no helper
// is configured, on any non-darwin platform, for a source carrying a video tap
// (a paired camera keeps its ffmpeg demux), or for any source that is not an
// audio-only device.
func micCaptureCommand(goos, helper, src, videoDir string) (bin string, args []string, ok bool) {
	if helper == "" || videoDir != "" || goos != osDarwin {
		return "", nil, false
	}

	name, ok := micCaptureName(src)
	if !ok {
		return "", nil, false
	}

	return helper, micCaptureArgs(name), true
}

// micCaptureName returns the AVFoundation device name the helper should open
// for a pure audio-device source, preferring the display-name hint that
// survives index reshuffles over the positional id. A paired camera source and
// a video device are not audio-only captures and report false.
func micCaptureName(src string) (string, bool) {
	if !IsDeviceURL(src) || IsAVDeviceURL(src) {
		return "", false
	}

	kind, id, label, err := deviceParts(src)
	if err != nil || kind != kindAudio {
		return "", false
	}

	// A colon reads as AVFoundation's video:audio separator, so such labels
	// keep the positional id — mirroring deviceInputArgsFor.
	if label != "" && !strings.Contains(label, ":") {
		return label, true
	}

	return id, true
}

// micCaptureArgs is the helper's fixed invocation: one device at the reference
// sample rate. The helper streams s16le mono, matching ffmpeg's PCM contract.
func micCaptureArgs(name string) []string {
	return []string{"--device", name, "--rate", strconv.Itoa(pipeline.SampleRate)}
}
