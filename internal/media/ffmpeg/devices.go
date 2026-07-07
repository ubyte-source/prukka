package ffmpeg

import (
	"fmt"
	"runtime"
	"strings"
)

// device:// URLs map local devices onto ffmpeg's platform device layers;
// <id> is the platform's index or name (docs/DEVICES.md).
const deviceScheme = "device://"

// GOOS names used across the package's platform dispatch (goconst).
const (
	osDarwin  = "darwin"
	osLinux   = "linux"
	osWindows = "windows"
)

// IsDeviceURL reports whether a URL names a local device.
func IsDeviceURL(url string) bool {
	return strings.HasPrefix(url, deviceScheme)
}

// deviceParts splits device://<kind>/<id>.
func deviceParts(url string) (kind, id string, err error) {
	kind, id, found := strings.Cut(strings.TrimPrefix(url, deviceScheme), "/")
	if !found || kind == "" || id == "" {
		return "", "", fmt.Errorf("device URL %q: want device://audio/<id> or device://video/<id>", url)
	}

	return kind, id, nil
}

// deviceInputArgs builds the capture-side input for one device source.
func deviceInputArgs(url string) ([]string, error) {
	kind, id, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	if kind != "audio" {
		return nil, fmt.Errorf("device source %q: only audio capture is supported as a session source", url)
	}

	switch runtime.GOOS {
	case osDarwin:
		return []string{flagFormat, "avfoundation", flagInput, ":" + id}, nil
	case osWindows:
		return []string{flagFormat, "dshow", flagInput, "audio=" + id}, nil
	default: // linux and the BSDs
		return []string{flagFormat, "pulse", flagInput, id}, nil
	}
}

// DeviceOutputArgs builds the playback/injection side of a device push;
// platforms without a muxer report an honest error.
func DeviceOutputArgs(url string) ([]string, error) {
	kind, id, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	switch {
	case kind == "audio" && runtime.GOOS == osDarwin:
		return []string{"-c:a", "pcm_s16le", flagFormat, "audiotoolbox", "-audio_device_index", id, "-"}, nil
	case kind == "audio" && runtime.GOOS == osLinux:
		return []string{"-c:a", "pcm_s16le", flagFormat, "pulse", id}, nil
	case kind == "video" && runtime.GOOS == osLinux:
		return []string{"-pix_fmt", "yuv420p", flagFormat, "v4l2", id}, nil
	default:
		return nil, fmt.Errorf(
			"device target %q: no %s output on %s yet — install the platform's virtual device and see docs/DEVICES.md",
			url, kind, runtime.GOOS)
	}
}
