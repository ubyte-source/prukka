//go:build windows

package discover

import (
	"context"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/wasapi"
)

// Devices enumerates dshow capture devices through ffmpeg — dshow selects by
// friendly name — and render endpoints natively through WASAPI; layers that
// fail contribute nothing.
func Devices(ctx context.Context, bin string) []Device {
	var out []Device

	if bin != "" {
		raw, err := listCaptureRaw(ctx, bin, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
		if err == nil {
			audio, video := parseDShow(raw)
			for _, e := range audio {
				out = append(out, Device{
					URL:     deviceurl.Ref{Kind: deviceurl.Audio, ID: e.id}.String(),
					Label:   e.label,
					Kind:    AudioIn,
					Virtual: virtualLabel(e.label),
				})
			}

			for _, e := range video {
				out = append(out, Device{
					URL:     deviceurl.Ref{Kind: deviceurl.Video, ID: e.id}.String(),
					Label:   e.label,
					Kind:    VideoIn,
					Virtual: virtualLabel(e.label),
				})
			}
		}
	}

	defaultOutput := deviceurl.Ref{Kind: deviceurl.Audio, ID: wasapi.DefaultEndpointID}
	out = append(out, Device{URL: defaultOutput.String(), Label: "System default output", Kind: AudioOut})

	// Without endpoints the default output above keeps the picker usable.
	if endpoints, err := wasapi.Endpoints(); err == nil {
		for _, e := range endpoints {
			out = append(out, Device{
				URL:     deviceurl.Ref{Kind: deviceurl.Audio, ID: e.ID}.String(),
				Label:   e.Label,
				Kind:    AudioOut,
				Virtual: virtualLabel(e.Label),
			})
		}
	}

	return appendNativeVideoOutput(out, "Prukka Webcam", ffmpeg.NativeVideoAvailable(ctx))
}
