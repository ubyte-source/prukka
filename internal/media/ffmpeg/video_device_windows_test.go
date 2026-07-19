//go:build windows

package ffmpeg

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestNativeVideoControllerUsesProgramData(t *testing.T) {
	t.Setenv("ProgramData", `D:\SharedData`)

	root := `D:\SharedData`
	want := filepath.Join(root, "Prukka", "devices", "webcam", windowsCameraController)
	if got := nativeVideoController(); got != want {
		t.Fatalf("controller = %q, want %q", got, want)
	}
}

func TestWindowsCameraArgsProduceControllerFormat(t *testing.T) {
	t.Parallel()

	args := windowsCameraArgs(`C:\live\index.m3u8`)
	for _, token := range []string{"-re", "scale=1280:720,fps=30", "yuyv422", "rawvideo", pipeOut} {
		if !slices.Contains(args, token) {
			t.Fatalf("camera args %q omit %q", args, token)
		}
	}
}
