//go:build linux

package discover

import (
	"os"
	"path/filepath"
	"testing"
)

// TestV4L2CamerasReadKernelNames: capture nodes surface with their
// kernel-reported names and /dev paths.
func TestV4L2CamerasReadKernelNames(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "video0"), 0o750); err != nil {
		t.Fatalf("stage sysfs fixture: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "video0", "name"), []byte("Integrated Camera\n"), 0o600); err != nil {
		t.Fatalf("stage camera name: %v", err)
	}

	prev := sysfsVideo
	sysfsVideo = root

	t.Cleanup(func() { sysfsVideo = prev })

	got := v4l2Cameras()

	if len(got) != 1 {
		t.Fatalf("found %d cameras, want 1", len(got))
	}

	want := Device{URL: "device://video//dev/video0", Label: "Integrated Camera", Kind: VideoIn}
	if got[0] != want {
		t.Fatalf("camera = %+v, want %+v", got[0], want)
	}
}

func TestV4L2VirtualCameraIsAlsoAnOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "video9"), 0o750); err != nil {
		t.Fatalf("stage sysfs fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "video9", "name"), []byte("Prukka Camera\n"), 0o600); err != nil {
		t.Fatalf("stage camera name: %v", err)
	}

	prev := sysfsVideo
	sysfsVideo = root
	t.Cleanup(func() { sysfsVideo = prev })

	got := v4l2Cameras()
	if len(got) != 2 || got[0].Kind != VideoIn || got[1].Kind != VideoOut {
		t.Fatalf("virtual camera roles = %+v, want video input and output", got)
	}
	if !got[0].Virtual || !got[1].Virtual || got[0].URL != got[1].URL {
		t.Fatalf("virtual camera identity = %+v, want one shared virtual device", got)
	}
}

// TestDevicesWithoutFFmpegListsOnlyCameras: no ffmpeg means no pulse
// listing, never an error.
func TestDevicesWithoutFFmpegListsOnlyCameras(t *testing.T) {
	prev := sysfsVideo
	sysfsVideo = t.TempDir()
	t.Cleanup(func() { sysfsVideo = prev })

	devices, err := Devices(t.Context(), "")
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	for _, d := range devices {
		if d.Kind != VideoIn {
			t.Fatalf("audio device %+v listed without ffmpeg", d)
		}
	}
}
