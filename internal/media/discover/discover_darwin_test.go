//go:build darwin

package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestCoreAudioOutputsEnumerates: the CoreAudio walk returns well-formed
// playback devices on whatever hardware the host has (possibly none).
func TestCoreAudioOutputsEnumerates(t *testing.T) {
	t.Parallel()

	for _, d := range coreAudioOutputs() {
		if !strings.HasPrefix(d.URL, "device://audio/") || d.Label == "" || d.Kind != AudioOut {
			t.Fatalf("malformed output device: %+v", d)
		}
	}
}

// TestDevicesWithoutFFmpegStillListsOutputs: no ffmpeg means no capture
// list, never an error — playback enumeration is native.
func TestDevicesWithoutFFmpegStillListsOutputs(t *testing.T) {
	t.Parallel()

	devices, err := Devices(t.Context(), "")
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	for _, d := range devices {
		if d.Kind != AudioOut {
			t.Fatalf("capture device %+v listed without ffmpeg", d)
		}
	}
}

// TestMain lets the test binary impersonate the ffmpeg listing when
// re-exec'd through the symlink TestDevicesParsesCaptureListing plants.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "ffmpeg" {
		fmt.Fprint(os.Stderr, avfStubListing)
		os.Exit(1) // a listing run always exits non-zero (no real input)
	}

	os.Exit(m.Run())
}

// avfStubListing replays the real-world avfoundation listing shape.
const avfStubListing = `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] Stub Camera
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] Stub Microphone
[AVFoundation indev @ 0x1] [1] Prukka Microphone
`

// TestDevicesParsesCaptureListing: the ffmpeg branch turns a listing into
// capture devices; the impersonated binary replays the real-world output.
func TestDevicesParsesCaptureListing(t *testing.T) {
	t.Parallel()

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	stub := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.Symlink(exe, stub); err != nil {
		t.Fatalf("plant stub ffmpeg: %v", err)
	}

	devices, err := Devices(t.Context(), stub)
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	assertContains(t, devices, Device{
		URL: "device://audio/0?label=Stub+Microphone", Label: "Stub Microphone", Kind: AudioIn,
	})
	assertContains(t, devices, Device{URL: "device://video/0", Label: "Stub Camera", Kind: VideoIn})
	assertContains(t, devices, Device{
		URL: "device://audio/1?label=Prukka+Microphone", Label: "Prukka Microphone", Kind: AudioIn, Virtual: true,
	})
}

// TestOutputIndexTracksTheCurrentArray: the label lookup must agree with
// the enumeration it rebinds for — on whatever devices this machine has.
func TestOutputIndexTracksTheCurrentArray(t *testing.T) {
	t.Parallel()

	labels := map[int]string{}
	for _, d := range coreAudioOutputs() {
		raw := strings.TrimPrefix(d.URL, "device://audio/")
		id, _, _ := strings.Cut(raw, "?")
		index, err := strconv.Atoi(id)
		if err != nil {
			t.Fatalf("output URL %q has a non-numeric index", d.URL)
		}
		labels[index] = d.Label
	}
	if len(labels) == 0 {
		t.Skip("no output devices on this machine")
	}

	for _, label := range labels {
		index, ok := OutputIndex(label)
		if !ok {
			t.Fatalf("OutputIndex(%q) not found in the live array", label)
		}
		if labels[index] != label {
			t.Fatalf("OutputIndex(%q) = %d, which is %q", label, index, labels[index])
		}
	}
}

// assertContains fails unless want is among the enumerated devices.
func assertContains(t *testing.T, devices []Device, want Device) {
	t.Helper()

	if slices.Contains(devices, want) {
		return
	}

	t.Fatalf("devices = %+v, missing %+v", devices, want)
}
