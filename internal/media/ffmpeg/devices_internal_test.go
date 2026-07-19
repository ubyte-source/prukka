package ffmpeg

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

	prewarmHelper(t, helper)

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

// prewarmHelper runs the freshly written script once through the API under
// test: the FIRST exec pays Gatekeeper's assessment, which can take seconds
// on a loaded machine and would trip the measured close in the caller.
func prewarmHelper(t *testing.T, helper string) {
	t.Helper()
	warm, err := StartDevicePlayback(t.Context(), helper, "warm", 16000, discardLog())
	if err != nil {
		return
	}
	if closeErr := warm.Close(); closeErr != nil {
		t.Logf("warm close: %v", closeErr)
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestPlaybackSinkCloseKillsAStuckHelper exercises the bounded-drain kill
// branch: a helper that ignores its sealed stdin must be force-killed within
// the drain, never blocking Close.
func TestPlaybackSinkCloseKillsAStuckHelper(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("uses the POSIX sleep helper")
	}

	cmd := exec.CommandContext(t.Context(), "sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	sink := &playbackSink{stdin: stdin, cmd: cmd, drain: 20 * time.Millisecond}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- sink.Close() }()

	var closeErr error
	select {
	case closeErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked well past the drain: kill branch never fired")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %v; the 20ms drain should have killed the helper promptly", elapsed)
	}
	// A helper that could not drain is killed, so Wait reports the signal.
	if closeErr == nil {
		t.Fatal("Close returned nil; a killed helper's Wait must surface an error")
	}
}

// TestLineLoggerSplitsBuffersAndDropsBlankLines pins the stateful stderr
// line-splitter: partial lines carry across writes, multiple lines in one
// write each emit, blank/whitespace-only lines are dropped, and a trailing
// partial is held until its newline. Write always returns len(p).
func TestLineLoggerSplitsBuffersAndDropsBlankLines(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ll := &lineLogger{log: log, msg: "helper"}

	for _, w := range []string{"star", "ted\n", "a\n\nb\n", "  \n", "partial-no-nl"} {
		n, err := ll.Write([]byte(w))
		if err != nil {
			t.Fatalf("Write(%q): %v", w, err)
		}
		if n != len(w) {
			t.Fatalf("Write(%q) returned %d, want %d", w, n, len(w))
		}
	}

	out := buf.String()
	for _, want := range []string{"line=started", "line=a", "line=b"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "partial-no-nl") {
		t.Errorf("held partial line was emitted before its newline:\n%s", out)
	}
	if got := strings.Count(out, "msg=helper"); got != 3 {
		t.Errorf("emitted %d lines, want 3 (blank + whitespace-only dropped):\n%s", got, out)
	}
}
