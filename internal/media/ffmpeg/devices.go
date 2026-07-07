package ffmpeg

import (
	"context"
	"fmt"
	neturl "net/url"
	"os/exec"
	"runtime"
	"strconv"
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

// ffmpeg device layers named once (goconst).
const (
	fmtPulse        = "pulse"
	fmtAVFoundation = "avfoundation"
)

// Stream selectors shared by the demux invocations (goconst).
const (
	mapFirstAudio  = "0:a:0"
	mapFirstVideo  = "0:v:0"
	mapSecondAudio = "1:a:0"
)

// IsDeviceURL reports whether a URL names a local device.
func IsDeviceURL(url string) bool {
	return strings.HasPrefix(url, deviceScheme)
}

// ListRaw runs one ffmpeg device-listing invocation and returns everything
// it printed on either stream. Listings exit non-zero by design (no real
// input follows the flag), so the exit status is deliberately ignored;
// only a binary that could not run at all is an error.
func ListRaw(ctx context.Context, bin string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if len(out) == 0 && err != nil {
		return "", fmt.Errorf("list devices: %w", err)
	}

	return string(out), nil
}

// deviceParts splits device://<kind>/<id>[?label=<name>]. The label is the
// device's stable display name: positional ids reshuffle whenever any
// device appears or vanishes (OBS, AirPods, Continuity), so consumers
// rebind by label at start time whenever one is present.
func deviceParts(url string) (kind, id, label string, err error) {
	kind, id, found := strings.Cut(strings.TrimPrefix(url, deviceScheme), "/")
	if !found || kind == "" || id == "" {
		return "", "", "", fmt.Errorf("device URL %q: want device://audio/<id> or device://video/<id>", url)
	}

	if bare, query, tagged := strings.Cut(id, "?"); tagged {
		id = bare
		if values, parseErr := neturl.ParseQuery(query); parseErr == nil {
			label = values.Get("label")
		}
	}
	if id == "" {
		return "", "", "", fmt.Errorf("device URL %q: empty device id", url)
	}

	return kind, id, label, nil
}

// resolveOutputIndex maps an output device label to its current position
// in the system device array; main wires the platform lookup (this
// package cannot reach CoreAudio itself).
var resolveOutputIndex func(label string) (int, bool)

// SetOutputIndexResolver installs the label-to-current-index lookup used
// to rebind output targets when they start.
func SetOutputIndexResolver(resolver func(label string) (int, bool)) {
	resolveOutputIndex = resolver
}

// outputIndex prefers the label's current index over the embedded one.
func outputIndex(id, label string) string {
	if label != "" && resolveOutputIndex != nil {
		if fresh, ok := resolveOutputIndex(label); ok {
			return strconv.Itoa(fresh)
		}
	}

	return id
}

// avSource is a camera paired with a microphone: the capture input args,
// where each stream lives in the inputs, and — always, cameras deliver
// raw frames — a video leg that encodes instead of copying.
type avSource struct {
	audioMap string
	videoMap string
	input    []string
}

// IsAVDeviceURL reports a paired camera+microphone source.
func IsAVDeviceURL(url string) bool {
	return strings.HasPrefix(url, deviceScheme+"av/")
}

// deviceAV parses a device://av/<camera>|<microphone> source into its
// platform capture invocation.
func deviceAV(url string) (avSource, error) {
	_, id, _, err := deviceParts(url)
	if err != nil {
		return avSource{}, err
	}

	return avInputArgs(url, id)
}

// avParts splits the id of device://av/<video>|<audio>.
func avParts(url, id string) (video, audio string, err error) {
	video, audio, found := strings.Cut(id, "|")
	if !found || video == "" || audio == "" {
		return "", "", fmt.Errorf("device URL %q: want device://av/<camera>|<microphone>", url)
	}

	return video, audio, nil
}

// avInputArgs builds the combined camera+microphone capture for one
// platform: one avfoundation/dshow input on macOS/Windows, a v4l2 plus a
// pulse input on Linux (v4l2 nodes carry no audio).
func avInputArgs(url, id string) (avSource, error) {
	video, audio, err := avParts(url, id)
	if err != nil {
		return avSource{}, err
	}

	switch runtime.GOOS {
	case osDarwin:
		return avSource{
			input:    []string{flagFormat, fmtAVFoundation, "-framerate", "30", flagInput, video + ":" + audio},
			audioMap: mapFirstAudio, videoMap: mapFirstVideo,
		}, nil
	case osWindows:
		return avSource{
			input:    []string{flagFormat, "dshow", flagInput, "video=" + video + ":audio=" + audio},
			audioMap: mapFirstAudio, videoMap: mapFirstVideo,
		}, nil
	case osLinux:
		return avSource{
			input:    []string{flagFormat, "v4l2", flagInput, video, flagFormat, fmtPulse, flagInput, audio},
			audioMap: mapSecondAudio, videoMap: mapFirstVideo,
		}, nil
	default:
		return avSource{}, fmt.Errorf("device source %q: camera capture is not supported on %s", url, runtime.GOOS)
	}
}

// deviceInputArgs builds the capture-side input for one device source.
func deviceInputArgs(url string) ([]string, error) {
	kind, id, label, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	if kind != "audio" {
		return nil, fmt.Errorf("device source %q: only audio capture is supported as a session source", url)
	}

	switch runtime.GOOS {
	case osDarwin:
		// avfoundation resolves names itself; a colon would read as its
		// video:audio separator, so such labels keep the index.
		if label != "" && !strings.Contains(label, ":") {
			return []string{flagFormat, fmtAVFoundation, flagInput, ":" + label}, nil
		}

		return []string{flagFormat, fmtAVFoundation, flagInput, ":" + id}, nil
	case osWindows:
		return []string{flagFormat, "dshow", flagInput, "audio=" + id}, nil
	default: // linux and the BSDs
		return []string{flagFormat, fmtPulse, flagInput, id}, nil
	}
}

// DeviceOutputArgs builds the playback/injection side of a device push;
// platforms without a muxer report an honest error.
func DeviceOutputArgs(url string) ([]string, error) {
	kind, id, label, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	switch {
	case kind == "audio" && runtime.GOOS == osDarwin:
		return []string{
			"-c:a", "pcm_s16le", flagFormat, "audiotoolbox",
			"-audio_device_index", outputIndex(id, label), "-",
		}, nil
	case kind == "audio" && runtime.GOOS == osLinux:
		// The pulse muxer's URL is only the stream NAME shown in mixers;
		// the sink is chosen by -device (default sink otherwise).
		return []string{"-c:a", "pcm_s16le", flagFormat, fmtPulse, "-device", id, "prukka-dub"}, nil
	case kind == "video" && runtime.GOOS == osLinux:
		return []string{"-pix_fmt", "yuv420p", flagFormat, "v4l2", id}, nil
	default:
		return nil, fmt.Errorf(
			"device target %q: no %s output on %s yet — install the platform's virtual device and see docs/DEVICES.md",
			url, kind, runtime.GOOS)
	}
}
