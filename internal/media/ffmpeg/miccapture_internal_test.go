package ffmpeg

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestMicCaptureNameResolvesAudioDevices(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want string
		ok   bool
	}{
		{name: "label preferred", src: "device://audio/2?label=Built-in Microphone", want: "Built-in Microphone", ok: true},
		{name: "bare index without a label", src: "device://audio/2", want: "2", ok: true},
		{name: "colon label keeps the index", src: "device://audio/2?label=0:1", want: "2", ok: true},
		{name: "paired camera is not audio-only", src: "device://av/FaceTime|Built-in Microphone", ok: false},
		{name: "video device", src: "device://video/0", ok: false},
		{name: "network source", src: "rtmp://host/app/key", ok: false},
		{name: "malformed device url", src: "device://", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := micCaptureName(tc.src)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("micCaptureName(%q) = %q,%v; want %q,%v", tc.src, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestMicCaptureCommandRoutesOnlyDarwinAudioCaptures(t *testing.T) {
	t.Parallel()

	const helper = "/opt/prukka/prukka-miccapture"
	const mic = "device://audio/2?label=Built-in Microphone"
	wantArgs := []string{"--device", "Built-in Microphone", "--rate", strconv.Itoa(core.SampleRate)}

	cases := []struct {
		name     string
		goos     string
		helper   string
		src      string
		videoDir string
		wantOK   bool
	}{
		{name: "darwin audio device", goos: osDarwin, helper: helper, src: mic, wantOK: true},
		{name: "no helper configured", goos: osDarwin, helper: "", src: mic, wantOK: false},
		{name: "video tap keeps ffmpeg", goos: osDarwin, helper: helper, src: mic, videoDir: "/tmp/v", wantOK: false},
		{name: "non-darwin platform", goos: osLinux, helper: helper, src: mic, wantOK: false},
		{name: "network source", goos: osDarwin, helper: helper, src: "srt://host:9000", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bin, args, ok := micCaptureCommand(tc.goos, tc.helper, tc.src, tc.videoDir)
			if ok != tc.wantOK {
				t.Fatalf("micCaptureCommand ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if bin != tc.helper || !slices.Equal(args, wantArgs) {
				t.Fatalf("micCaptureCommand = %q %v, want %q %v", bin, args, tc.helper, wantArgs)
			}
		})
	}
}

func TestWithMicCaptureConfiguresSupervisor(t *testing.T) {
	t.Parallel()

	const helper = "/opt/prukka/prukka-miccapture"
	sup := NewSupervisor("ffmpeg", slog.New(slog.DiscardHandler), WithMicCapture(helper))
	if sup.micCapture != helper {
		t.Fatalf("micCapture = %q, want %q", sup.micCapture, helper)
	}

	plain := NewSupervisor("ffmpeg", slog.New(slog.DiscardHandler))
	if plain.micCapture != "" {
		t.Fatalf("default micCapture = %q, want empty", plain.micCapture)
	}
}

// TestStartPCMSpawnsMicCaptureHelper proves the integration end to end: with a
// helper configured, a macOS audio-device source is captured by the helper —
// not the (deliberately unusable) ffmpeg binary — and its stdout flows through
// unchanged as the PCM pipe.
func TestStartPCMSpawnsMicCaptureHelper(t *testing.T) {
	if runtime.GOOS != osDarwin {
		t.Skip("native microphone capture is a macOS-only path")
	}
	t.Parallel()

	helper := writeFakeMicCapture(t)
	sup := NewSupervisor("/nonexistent/ffmpeg", slog.New(slog.DiscardHandler), WithMicCapture(helper))

	pcm, err := sup.StartPCM(t.Context(), "device://audio/2?label=Test Mic", "", 0)
	if err != nil {
		t.Fatalf("StartPCM: %v", err)
	}
	defer func() {
		if closeErr := pcm.Close(); closeErr != nil {
			t.Errorf("helper exited with %v, want clean stop", closeErr)
		}
	}()

	got, err := io.ReadAll(pcm)
	if err != nil {
		t.Fatalf("read helper PCM: %v", err)
	}
	if !reflect.DeepEqual(got, micCaptureHelperPCM) {
		t.Fatalf("helper PCM = %v, want %v", got, micCaptureHelperPCM)
	}
}

// micCaptureHelperPCM is the fixed s16le payload the fake helper writes,
// standing in for a real capture: samples 100, 200, 300, 400 little-endian.
var micCaptureHelperPCM = []byte{100, 0, 200, 0, 0x2C, 0x01, 0x90, 0x01}

// fakeMicCaptureName is the symlink basename StartPCM spawns; TestMain
// recognizes it and emits micCaptureHelperPCM instead of running the suite.
const fakeMicCaptureName = "prukka-miccapture-fake"

// writeFakeMicCapture plants a symlink to the test binary under the fake
// helper's name, so StartPCM re-execs this binary (already executable) rather
// than a written script — no file-permission fixture, no gosec suppression.
func writeFakeMicCapture(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}

	link := filepath.Join(t.TempDir(), fakeMicCaptureName)
	if linkErr := os.Symlink(exe, link); linkErr != nil {
		t.Fatalf("plant fake helper: %v", linkErr)
	}

	return link
}

// TestMicCaptureBinaryFallsBackToBundleRoot: the managed runtime ships the
// helper inside the bundle root; resolution must find it there when nothing
// sits beside the executable, and must ignore a non-executable candidate.
func TestMicCaptureBinaryFallsBackToBundleRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if runtime.GOOS != "darwin" {
		if got := MicCaptureBinary(root); got != "" {
			t.Fatalf("non-darwin resolution = %q, want empty", got)
		}

		return
	}

	path := filepath.Join(root, micCaptureBinaryName)
	writeHelper := func(mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	writeHelper(0o700)
	if got := MicCaptureBinary(root); got != path {
		t.Fatalf("bundle-root resolution = %q, want %q", got, path)
	}

	writeHelper(0o600)
	if got := MicCaptureBinary(root); got != "" {
		t.Fatalf("non-executable helper resolved to %q", got)
	}
}
