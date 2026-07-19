// Package discover enumerates the machine's capture and playback devices
// so pickers can offer real device names instead of raw device:// URLs.
// Enumeration is best-effort: whatever layer answers contributes, and an
// empty result simply sends the UI back to manual entry.
package discover

import "strings"

// Kind classifies a device's role.
type Kind string

// Device roles exposed by the discovery API.
const (
	AudioIn  Kind = "audio-in"
	VideoIn  Kind = "video-in"
	AudioOut Kind = "audio-out"
	VideoOut Kind = "video-out"
)

// Device is one local media device, ready to use where the daemon accepts
// a device:// URL.
type Device struct {
	// URL is the session source or push target, e.g. "device://audio/1".
	URL string
	// Label is the human-readable device name.
	Label string
	// Kind is the device's role.
	Kind Kind
	// Virtual marks Prukka's own loopback devices.
	Virtual bool
}

// virtualLabel recognizes Prukka's own loopback devices by name.
func virtualLabel(label string) bool {
	return strings.Contains(label, "Prukka")
}

func appendNativeVideoOutput(devices []Device, label string, available bool) []Device {
	if !available {
		return devices
	}

	return append(devices, Device{
		URL: "device://video/prukka", Label: label, Kind: VideoOut, Virtual: true,
	})
}
