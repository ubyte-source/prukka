//go:build linux

package discover

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

// sysfsVideo is where the kernel names V4L2 capture nodes; a variable so
// tests can point it at a fixture tree.
var sysfsVideo = "/sys/class/video4linux"

// Devices enumerates PulseAudio sources and sinks through ffmpeg — the names
// are what device:// uses on Linux — and cameras from sysfs; layers that fail
// contribute nothing.
func Devices(ctx context.Context, bin string) []Device {
	var out []Device

	if bin != "" {
		if raw, err := listCaptureRaw(ctx, bin, "-hide_banner", "-sources", "pulse"); err == nil {
			for _, e := range parsePulse(raw) {
				out = append(out, Device{
					URL:     deviceurl.Ref{Kind: deviceurl.Audio, ID: e.id}.String(),
					Label:   e.label,
					Kind:    AudioIn,
					Virtual: virtualLabel(e.label),
				})
			}
		}

		if raw, err := listCaptureRaw(ctx, bin, "-hide_banner", "-sinks", "pulse"); err == nil {
			for _, e := range parsePulse(raw) {
				out = append(out, Device{
					URL:     deviceurl.Ref{Kind: deviceurl.Audio, ID: e.id}.String(),
					Label:   e.label,
					Kind:    AudioOut,
					Virtual: virtualLabel(e.label),
				})
			}
		}
	}

	return append(out, v4l2Cameras()...)
}

// v4l2Cameras lists capture nodes by their kernel-reported names.
func v4l2Cameras() []Device {
	nodes, err := filepath.Glob(filepath.Join(sysfsVideo, "video*"))
	if err != nil {
		return nil
	}

	var out []Device

	for _, node := range nodes {
		name, readErr := os.ReadFile(filepath.Clean(filepath.Join(node, "name")))
		if readErr != nil {
			continue
		}

		label := strings.TrimSpace(string(name))
		dev := "/dev/" + filepath.Base(node)

		device := Device{
			URL:     deviceurl.Ref{Kind: deviceurl.Video, ID: dev}.String(),
			Label:   label,
			Kind:    VideoIn,
			Virtual: virtualLabel(label),
		}
		out = append(out, device)
		if device.Virtual {
			device.Kind = VideoOut
			out = append(out, device)
		}
	}

	return out
}
