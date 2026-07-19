package ffmpeg

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDeviceTargetLabel: only labeled audio device targets yield a rebinding
// name; everything else keeps the ffmpeg path.
func TestDeviceTargetLabel(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"device://audio/3?label=Prukka+Microphone": "Prukka Microphone",
		"device://audio/3":                         "",
		"device://video/0?label=Cam":               "",
		"rtmp://host/app":                          "",
		"::bad::":                                  "",
	}
	for target, want := range cases {
		if got := DeviceTargetLabel(target); got != want {
			t.Errorf("DeviceTargetLabel(%q) = %q, want %q", target, got, want)
		}
	}
}

// TestStartDevicePlaybackFeedsAndSeals: PCM written to the sink reaches the
// helper's stdin, and Close seals the pipe and reaps the process idempotently.
func TestStartDevicePlaybackFeedsAndSeals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake helpers cannot exec on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	captured := filepath.Join(dir, "out.pcm")
	helper := filepath.Join(dir, "helper")
	mode := os.FileMode(0o700)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec cat > \""+captured+"\"\n"), mode); err != nil {
		t.Fatal(err)
	}

	sink, err := StartDevicePlayback(t.Context(), helper, "Prukka Microphone", 16000, discardLog())
	if err != nil {
		t.Fatalf("StartDevicePlayback: %v", err)
	}
	payload := []byte("pcm-payload")
	if _, writeErr := sink.Write(payload); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("second close must stay idempotent: %v", closeErr)
	}
	data, readErr := os.ReadFile(filepath.Clean(captured))
	if readErr != nil || !bytes.Equal(data, payload) {
		t.Fatalf("helper captured %q (%v), want %q", data, readErr, payload)
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
