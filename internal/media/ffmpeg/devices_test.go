package ffmpeg

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestIsDeviceURL(t *testing.T) {
	t.Parallel()

	if !IsDeviceURL("device://audio/0") || IsDeviceURL("rtmp://x") {
		t.Fatal("device scheme detection is wrong")
	}
}

func TestDeviceInputArgsAudio(t *testing.T) {
	t.Parallel()

	args, err := deviceInputArgs("device://audio/0")
	if err != nil {
		t.Fatalf("audio input: %v", err)
	}

	// The platform demuxer must be selected and the id must reach -i.
	joined := strings.Join(args, " ")

	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(joined, "avfoundation") || !slices.Contains(args, ":0") {
			t.Fatalf("darwin capture args wrong: %v", args)
		}
	case "windows":
		if !strings.Contains(joined, "dshow") {
			t.Fatalf("windows capture args wrong: %v", args)
		}
	default:
		if !strings.Contains(joined, "pulse") {
			t.Fatalf("linux capture args wrong: %v", args)
		}
	}
}

func TestDeviceInputArgsRejectsVideoAndMalformed(t *testing.T) {
	t.Parallel()

	if _, err := deviceInputArgs("device://video/0"); err == nil {
		t.Fatal("video capture accepted as a session source")
	}

	if _, err := deviceInputArgs("device://audio"); err == nil {
		t.Fatal("malformed device URL accepted")
	}
}

func TestDeviceOutputArgsAudio(t *testing.T) {
	t.Parallel()

	audio, err := DeviceOutputArgs("device://audio/1")

	switch runtime.GOOS {
	case "darwin":
		if err != nil || !strings.Contains(strings.Join(audio, " "), "audiotoolbox") {
			t.Fatalf("darwin audio out: %v (%v)", audio, err)
		}
	case "linux":
		if err != nil || !strings.Contains(strings.Join(audio, " "), "pulse") {
			t.Fatalf("linux audio out: %v (%v)", audio, err)
		}
	default:
		if err == nil {
			t.Fatal("unsupported platform must error, not no-op")
		}
	}
}

func TestDeviceOutputArgsVideo(t *testing.T) {
	t.Parallel()

	video, err := DeviceOutputArgs("device://video//dev/video10")

	if runtime.GOOS == "linux" {
		if err != nil || !strings.Contains(strings.Join(video, " "), "v4l2") {
			t.Fatalf("linux video out: %v (%v)", video, err)
		}

		return
	}

	if err == nil {
		t.Fatal("video out must report unsupported honestly off linux")
	}
}
