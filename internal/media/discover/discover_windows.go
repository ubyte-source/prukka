//go:build windows

package discover

import (
	"context"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/wasapi"
)

// Devices enumerates dshow capture devices through ffmpeg (dshow selects
// by friendly name) and render endpoints natively through WASAPI.
func Devices(ctx context.Context, bin string) ([]Device, error) {
	var out []Device

	if bin != "" {
		raw, err := ffmpeg.ListRaw(ctx, bin, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
		if err == nil {
			audio, video := parseDShow(raw)
			for _, e := range audio {
				out = append(out, Device{
					URL: "device://audio/" + e.id, Label: e.label, Kind: AudioIn, Virtual: virtualLabel(e.label),
				})
			}

			for _, e := range video {
				out = append(out, Device{
					URL: "device://video/" + e.id, Label: e.label, Kind: VideoIn, Virtual: virtualLabel(e.label),
				})
			}
		}
	}

	out = append(out, Device{URL: "device://audio/default", Label: "System default output", Kind: AudioOut})

	// Endpoint enumeration is best-effort: without it the default output
	// above keeps the picker usable.
	if endpoints, err := wasapi.Endpoints(); err == nil {
		for _, e := range endpoints {
			out = append(out, Device{
				URL: "device://audio/" + e.ID, Label: e.Label, Kind: AudioOut, Virtual: virtualLabel(e.Label),
			})
		}
	}

	return appendNativeVideoOutput(out, "Prukka Webcam", ffmpeg.NativeVideoAvailable(ctx)), nil
}

// OutputIndex is a darwin concern: WASAPI endpoints are addressed by
// stable IDs, never positional indexes.
func OutputIndex(string) (int, bool) {
	return 0, false
}
